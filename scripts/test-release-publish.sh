#!/bin/bash
set -e

# Test script for release publish command
echo "Testing devkit avs release publish command"

# Check prerequisites
check_prerequisites() {
    echo "Checking prerequisites..."
    
    # Check if Docker is running
    if ! docker info &>/dev/null; then
        echo "Error: Docker is not running"
        exit 1
    fi
    
    # Check if devnet is running
    if ! nc -z localhost 8545 &>/dev/null; then
        echo "Error: Devnet is not running. Please run 'devkit avs devnet start' first"
        exit 1
    fi
    
    # Check if yq is installed
    if ! command -v yq &> /dev/null; then
        echo "Error: yq not found"
        exit 1
    fi
    
    # Check if cast is installed
    if ! command -v cast &> /dev/null; then
        echo "Error: cast (Foundry) is not installed"
        exit 1
    fi
    
    echo "Prerequisites met"
}

# Start local Docker registry
start_registry() {
    echo "Starting local Docker registry..."
    
    # Stop any existing registry
    docker stop test-registry &>/dev/null || true
    docker rm test-registry &>/dev/null || true
    
    # Start new registry
    docker run -d --name test-registry -p 5001:5000 registry:2
    
    # Wait for registry to be ready
    for i in {1..10}; do
        if nc -z localhost 5001; then
            echo "Registry is ready"
            return 0
        fi
        echo "Waiting for registry... ($i/10)"
        sleep 2
    done
    
    echo "Error: Registry failed to start"
    exit 1
}

# Build and push AVS image
build_and_push_image() {
    echo "Building and pushing AVS image..."
    
    # Build the image
    docker build -t localhost:5001/my-awesome-avs:test .
    
    # Push to local registry
    docker push localhost:5001/my-awesome-avs:test
    
    echo "Image pushed to local registry"
}

# Update context configuration
update_context() {
    echo "Updating context configuration..."
    
    # Backup original context
    cp config/contexts/devnet.yaml config/contexts/devnet.yaml.bak
    
    # Update registry to use local registry
    yq eval '.context.artifact.registry = "localhost:5001"' -i config/contexts/devnet.yaml
}

# Check AVS deployment status
check_avs_deployment() {
    echo "Checking AVS deployment status..."
    
    # Get AVS address
    AVS_ADDRESS=$(yq eval '.context.avs.address' config/contexts/devnet.yaml)
    if [ -z "$AVS_ADDRESS" ] || [ "$AVS_ADDRESS" = "null" ]; then
        echo "Error: AVS address not found in context"
        return 1
    fi
    
    echo "AVS Address: $AVS_ADDRESS"
    
    # Check if AVS contract has code
    CODE=$(cast code $AVS_ADDRESS --rpc-url http://localhost:8545)
    if [ "$CODE" = "0x" ]; then
        echo "Error: AVS is not deployed at $AVS_ADDRESS"
        return 1
    fi
    
    echo "AVS is deployed"
}

# Publish release
publish_release() {
    echo "Publishing release..."
    
    # Get current timestamp + 1 hour
    UPGRADE_BY_TIME=$(($(date +%s) + 3600))
    echo "Upgrade by time: $UPGRADE_BY_TIME"
    
    # Run release publish command
    if devkit avs release publish --upgrade-by-time $UPGRADE_BY_TIME --registry localhost:5001; then
        echo "Release publish command completed"
        # Wait for transaction to be mined
        sleep 5
    else
        echo "Error: Release publish failed"
        return 1
    fi
}

# Verify release on chain
verify_release() {
    echo "Verifying release on ReleaseManager contract..."
    
    # Get addresses
    AVS_ADDRESS=$(yq eval '.context.avs.address' config/contexts/devnet.yaml)
    RELEASE_MANAGER_ADDRESS="0x323A9FcB2De80d04B5C4B0F72ee7799100D32F0F"
    
    echo "AVS Address: $AVS_ADDRESS"
    echo "ReleaseManager Address: $RELEASE_MANAGER_ADDRESS"
    
    # Check if ReleaseManager is deployed
    CODE=$(cast code $RELEASE_MANAGER_ADDRESS --rpc-url http://localhost:8545)
    if [ "$CODE" = "0x" ]; then
        echo "Error: ReleaseManager not deployed"
        echo "Cannot verify contract state as required"
        return 1
    fi
    
    # Get latest version
    LATEST_VERSION=$(cast call $RELEASE_MANAGER_ADDRESS \
        "latestVersion(address,uint32)" \
        $AVS_ADDRESS 0 \
        --rpc-url http://localhost:8545)
    
    VERSION_DEC=$((LATEST_VERSION))
    echo "Latest version: $VERSION_DEC"
    
    if [ $VERSION_DEC -eq 0 ]; then
        echo "Error: No release found on ReleaseManager"
        return 1
    fi
    
    # Get release details
    RELEASE_DATA=$(cast call $RELEASE_MANAGER_ADDRESS \
        "getRelease(address,uint32,uint256)" \
        $AVS_ADDRESS 0 $((VERSION_DEC - 1)) \
        --rpc-url http://localhost:8545)
    
    if [ -z "$RELEASE_DATA" ] || [ "$RELEASE_DATA" = "0x" ]; then
        echo "Error: Release data is empty"
        return 1
    fi
    
    echo "Release data: ${RELEASE_DATA:0:66}..."
    echo "Release verified on chain"
}

# Cleanup
cleanup() {
    echo "Cleaning up..."
    
    # Stop registry
    docker stop test-registry &>/dev/null || true
    docker rm test-registry &>/dev/null || true
    
    # Restore original context if backup exists
    if [ -f config/contexts/devnet.yaml.bak ]; then
        mv config/contexts/devnet.yaml.bak config/contexts/devnet.yaml
    fi
}

# Main execution
main() {
    # Ensure we're in the AVS project directory
    if [ ! -f "config/config.yaml" ]; then
        echo "Error: Not in an AVS project directory"
        exit 1
    fi
    
    # Set trap for cleanup on exit
    trap cleanup EXIT
    
    # Run test steps
    check_prerequisites
    check_avs_deployment
    start_registry
    build_and_push_image
    update_context
    publish_release
    verify_release
    
    echo "All tests passed"
}

# Run main function
main "$@" 