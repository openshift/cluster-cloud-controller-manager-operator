#!/bin/bash

set -e

function vendor_workspace() {
  echo "Updating dependencies for Cluster Cloud Controller Manager Operator workspace"

  go work use -r .
  go work edit -dropuse ./cmd/cloud-controller-manager-aws-tests-ext

  # Discover all modules from the workspace
  echo "Discovering modules from workspace..."
  MODULES=$(go work edit -json | grep -o '"DiskPath": "[^"]*"' | cut -d'"' -f4 | sed 's|^\./||')
  echo "Found modules: $MODULES"

  # Pass 1: tidy all modules
  echo "Running go mod tidy for all modules (pass 1)..."
  for module in $MODULES; do
    if [ -f "$module/go.mod" ]; then
      echo "Tidying $module"
      (cd "$module" && go mod tidy)
    fi
  done

  # Sync: propagate highest require versions across all modules
  echo "Syncing Go workspace..."
  go work sync

  # Pass 2: re-tidy after sync may have bumped versions
  echo "Running go mod tidy for all modules (pass 2)..."
  for module in $MODULES; do
    if [ -f "$module/go.mod" ]; then
      echo "Tidying $module"
      (cd "$module" && go mod tidy)
    fi
  done

  # Verify all modules
  echo "Verifying all modules..."
  for module in $MODULES; do
    if [ -f "$module/go.mod" ]; then
      echo "Verifying $module"
      (cd "$module" && go mod verify)
    fi
  done

  # Create unified vendor directory
  echo "Creating unified vendor directory..."
  go work vendor -v

  echo "Done updating dependencies for Cluster Cloud Controller Manager Operator workspace"
}

# vendor_ote_ccmaws is used to update the dependencies for the CCM AWS tests.
# CCM-AWS OTE is outside of workspace to prevent cross-module dependency conflicts.
function vendor_ote_ccmaws() {
  echo "Updating dependencies for CCM AWS tests"
  cd cmd/cloud-controller-manager-aws-tests-ext
  GOWORK=off go mod tidy
  GOWORK=off go mod vendor
  cd ../..
  echo "Updated dependencies for CCM AWS tests"
}

vendor_ote_ccmaws
vendor_workspace
