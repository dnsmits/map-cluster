#!/bin/bash

set -euo pipefail

# Start all services in the Docker network
echo "Starting MapCluster services..."

docker compose up -d

echo ""
echo "✓ Services started successfully!"
echo ""
echo "Available services:"
echo "  - API:        http://localhost:8080"
echo "  - Frontend:   http://localhost:3000"
echo "  - pgAdmin:    http://localhost:5050"
echo ""
echo "Database credentials (pgAdmin):"
echo "  Email:    admin@example.com"
echo "  Password: admin"
echo ""
echo "To stop all services, run: docker compose down"
echo "To view logs, run: docker compose logs -f"
