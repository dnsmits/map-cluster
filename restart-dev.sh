#!/bin/bash

set -euo pipefail

# Recreate services so local code or Dockerfile changes are picked up
echo "Restarting MapCluster services with rebuild..."

docker compose up -d --build --force-recreate

echo ""
echo "✓ Services restarted successfully!"
echo ""
echo "Available services:"
echo "  - API:        http://localhost:8080"
echo "  - Frontend:   http://localhost:3000"
echo "  - pgAdmin:    http://localhost:5050"
echo ""
echo "To stop all services, run: ./teardown-dev.sh"
echo "To view logs, run: docker compose logs -f"
