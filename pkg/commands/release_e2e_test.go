package commands

import (
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Layr-Labs/devkit-cli/pkg/common"
	"github.com/Layr-Labs/devkit-cli/pkg/common/devnet"
	"github.com/Layr-Labs/devkit-cli/pkg/testutils"
	releasemanager "github.com/Layr-Labs/eigenlayer-contracts/pkg/bindings/ReleaseManager"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"
)

// setupDockerRegistry starts a local Docker registry at localhost:5001
func setupDockerRegistry(t *testing.T) func() {
	t.Helper()

	// Check if registry is already running
	cmd := exec.Command("docker", "ps", "-q", "-f", "name=devkit-test-registry")
	output, _ := cmd.Output()
	if len(output) > 0 {
		t.Log("Registry already running, using existing instance")
		return func() {
			// Don't stop the registry if it was already running
		}
	}

	// Start Docker registry
	cmd = exec.Command("docker", "run", "-d", "--rm",
		"--name", "devkit-test-registry",
		"-p", "5001:5000",
		"registry:2")

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check if it's already running (in case of race condition)
		if strings.Contains(string(output), "already in use") {
			t.Log("Registry already running")
			return func() {}
		}
		t.Fatalf("Failed to start Docker registry: %v, output: %s", err, string(output))
	}

	containerID := strings.TrimSpace(string(output))
	t.Logf("Started Docker registry with container ID: %s", containerID)

	// Wait for registry to be ready
	for i := 0; i < 30; i++ {
		cmd := exec.Command("curl", "-s", "http://localhost:5001/v2/")
		if err := cmd.Run(); err == nil {
			t.Log("Docker registry is ready")
			break
		}
		if i == 29 {
			t.Fatal("Docker registry failed to start within 30 seconds")
		}
		time.Sleep(1 * time.Second)
	}

	// Return cleanup function
	return func() {
		cmd := exec.Command("docker", "stop", "devkit-test-registry")
		if err := cmd.Run(); err != nil {
			t.Logf("Failed to stop Docker registry: %v", err)
		}
	}
}

// queryReleaseFromContract queries the ReleaseManager contract to verify the published release
func queryReleaseFromContract(t *testing.T, avsAddress string, operatorSetId uint32) (*releasemanager.IReleaseManagerTypesRelease, error) {
	// Load config to get RPC URL and contract addresses
	cfg, err := common.LoadConfigWithContextConfig(devnet.DEVNET_CONTEXT)
	require.NoError(t, err)

	envCtx, ok := cfg.Context[devnet.DEVNET_CONTEXT]
	require.True(t, ok)

	l1Cfg, ok := envCtx.Chains[devnet.L1]
	require.True(t, ok)

	// Connect to Ethereum client
	client, err := ethclient.Dial(l1Cfg.RPCURL)
	require.NoError(t, err)
	defer client.Close()

	// Get ReleaseManager address
	_, _, _, _, _, _, releaseManagerAddress := devnet.GetEigenLayerAddresses(cfg)
	require.NotEmpty(t, releaseManagerAddress)

	// Create ReleaseManager contract instance
	releaseManagerContract, err := releasemanager.NewReleaseManager(
		ethcommon.HexToAddress(releaseManagerAddress),
		client,
	)
	require.NoError(t, err)

	// Query the latest release
	operatorSet := releasemanager.OperatorSet{
		Avs: ethcommon.HexToAddress(avsAddress),
		Id:  operatorSetId,
	}

	// Get the latest release number
	latestReleaseNum, err := releaseManagerContract.GetLatestReleaseNum(&bind.CallOpts{}, operatorSet)
	require.NoError(t, err)

	// If no releases, return nil
	if latestReleaseNum == 0 {
		return nil, nil
	}

	// Get the release details
	release, err := releaseManagerContract.GetRelease(&bind.CallOpts{}, operatorSet, latestReleaseNum)
	require.NoError(t, err)

	return &release, nil
}

// TestReleasePublishE2E tests the complete release publish flow
func TestReleasePublishE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	// Check if devnet is running
	cmd := exec.Command("nc", "-z", "localhost", "8545")
	if err := cmd.Run(); err != nil {
		t.Skip("Devnet not running, skipping E2E test")
	}

	// Setup Docker registry
	cleanupRegistry := setupDockerRegistry(t)
	defer cleanupRegistry()

	// Create test project directory
	testDir := t.TempDir()
	projectName := "test-avs-release"
	projectDir := filepath.Join(testDir, projectName)

	// Create AVS project
	t.Log("Creating AVS project...")
	createApp := cli.NewApp()
	createApp.Commands = []*cli.Command{
		{
			Name: "avs",
			Subcommands: []*cli.Command{
				CreateCommand,
			},
		},
	}

	createCtx := testutils.SetupCLIContext(createApp, []string{"devkit", "avs", "create", projectName, "--dir", testDir, "--disable-telemetry"})
	err := createApp.RunContext(createCtx, []string{"devkit", "avs", "create", projectName, "--dir", testDir, "--disable-telemetry"})
	require.NoError(t, err)

	// Change to project directory
	originalWd, err := os.Getwd()
	require.NoError(t, err)
	err = os.Chdir(projectDir)
	require.NoError(t, err)
	defer os.Chdir(originalWd)

	// Build the AVS
	t.Log("Building AVS...")
	buildApp := cli.NewApp()
	buildApp.Commands = []*cli.Command{
		{
			Name: "avs",
			Subcommands: []*cli.Command{
				BuildCommand,
			},
		},
	}

	buildCtx := testutils.SetupCLIContext(buildApp, []string{"devkit", "avs", "build", "--disable-telemetry"})
	err = buildApp.RunContext(buildCtx, []string{"devkit", "avs", "build", "--disable-telemetry"})
	require.NoError(t, err)

	// Load config to get AVS address
	cfg, err := common.LoadConfigWithContextConfig(devnet.DEVNET_CONTEXT)
	require.NoError(t, err)
	avsAddress := cfg.Context[devnet.DEVNET_CONTEXT].Avs.Address
	require.NotEmpty(t, avsAddress)

	// Push image to local registry
	t.Log("Pushing image to local registry...")
	imageName := fmt.Sprintf("%s:0", projectName)
	localRegistryImage := fmt.Sprintf("localhost:5001/%s", imageName)

	// Tag the image
	tagCmd := exec.Command("docker", "tag", imageName, localRegistryImage)
	output, err := tagCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to tag image: %v, output: %s", err, string(output))
	}

	// Push to local registry
	pushCmd := exec.Command("docker", "push", localRegistryImage)
	output, err = pushCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to push image: %v, output: %s", err, string(output))
	}
	t.Logf("Successfully pushed image to local registry: %s", localRegistryImage)

	// Update the registry in config/contexts/devnet.yaml to use localhost:5001
	contextPath := filepath.Join("config", "contexts", "devnet.yaml")
	contextNode, err := common.LoadYAML(contextPath)
	require.NoError(t, err)

	rootNode := contextNode.Content[0]
	contextSection := common.GetChildByKey(rootNode, "context")
	require.NotNil(t, contextSection)

	artifactSection := common.GetChildByKey(contextSection, "artifact")
	if artifactSection != nil {
		common.SetMappingValue(artifactSection,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "registry"},
			&yaml.Node{Kind: yaml.ScalarNode, Value: "localhost:5001"})

		err = common.WriteYAML(contextPath, contextNode)
		require.NoError(t, err)
	}

	// Test release publish
	t.Log("Testing release publish...")

	// Set upgrade time to 1 hour from now
	upgradeByTime := time.Now().Add(1 * time.Hour).Unix()

	// Create release app
	releaseApp := cli.NewApp()
	releaseApp.Commands = []*cli.Command{
		{
			Name: "avs",
			Subcommands: []*cli.Command{
				ReleaseCommand,
			},
		},
	}

	// Execute release publish
	releaseCtx := testutils.SetupCLIContext(releaseApp, []string{
		"devkit", "avs", "release", "publish",
		"--upgrade-by-time", fmt.Sprintf("%d", upgradeByTime),
		"--registry", "localhost:5001",
		"--disable-telemetry",
	})

	err = releaseApp.RunContext(releaseCtx, []string{
		"devkit", "avs", "release", "publish",
		"--upgrade-by-time", fmt.Sprintf("%d", upgradeByTime),
		"--registry", "localhost:5001",
		"--disable-telemetry",
	})
	require.NoError(t, err)

	// Query ReleaseManager contract to verify the release
	t.Log("Verifying release on-chain...")

	// Wait a bit for transaction to be mined
	time.Sleep(5 * time.Second)

	// Query for operator set 0 (default)
	release, err := queryReleaseFromContract(t, avsAddress, 0)
	require.NoError(t, err)
	require.NotNil(t, release, "No release found on-chain")

	// Verify release details
	assert.Equal(t, uint32(upgradeByTime), release.UpgradeByTime, "UpgradeByTime mismatch")
	assert.Greater(t, len(release.Artifacts), 0, "No artifacts in release")

	// Verify artifact details
	for i, artifact := range release.Artifacts {
		t.Logf("Artifact %d:", i)
		t.Logf("  Registry URL: %s", artifact.RegistryUrl)
		t.Logf("  Digest: %s", hex.EncodeToString(artifact.Digest[:]))

		// Check if localhost:5001 is in the registry URL
		if strings.Contains(artifact.RegistryUrl, "localhost:5001") {
			assert.Contains(t, artifact.RegistryUrl, projectName, "Project name not in registry URL")
		}
	}

	t.Log("Release publish E2E test completed successfully!")
}

// TestReleasePublishWithMultipleOperatorSets tests publishing releases for multiple operator sets
func TestReleasePublishWithMultipleOperatorSets(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	// This test would require setting up multiple operator sets
	// and testing the release publish for each one
	// For now, we'll skip this as it requires more complex setup
	t.Skip("Multiple operator sets test not implemented yet")
}
