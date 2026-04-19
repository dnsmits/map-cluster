#!/bin/bash

set -euo pipefail

# Stop and remove all services defined in docker-compose.yml
echo "Tearing down MapCluster services..."

docker compose down --remove-orphans

echo ""
echo "✓ Services stopped and removed successfully!"
echo ""
echo "If you also want to remove persistent volumes, run:"
echo "  docker compose down --remove-orphans -v"
