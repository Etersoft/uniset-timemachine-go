#!/bin/bash
# InfluxDB initialization script
# Creates minimal test data for timemachine startup

set -e

# Wait for InfluxDB to be ready
until curl -s http://localhost:8086/ping > /dev/null 2>&1; do
    echo "Waiting for InfluxDB..."
    sleep 1
done

echo "InfluxDB is ready, creating test database..."

# Create database
curl -s -XPOST 'http://localhost:8086/query' --data-urlencode "q=CREATE DATABASE uniset" || true

# Create minimal seed data (just a few points to verify the setup works)
# For full data generation, use: make influx-gen-data
START_TS=$(date -d "7 days ago" +%s)000000000

echo "Creating seed data..."
cat <<EOF | curl -s -XPOST 'http://localhost:8086/write?db=uniset&precision=ns' --data-binary @-
AI0001_S value=10.5 ${START_TS}
AI0002_S value=20.3 ${START_TS}
AI0003_S value=30.1 ${START_TS}
AO0001_C value=15.0 ${START_TS}
DI001_S value=0 ${START_TS}
DI002_S value=1 ${START_TS}
DO001_C value=0 ${START_TS}
EOF

echo "InfluxDB initialization complete!"
echo "Database 'uniset' created with seed data."
echo ""
echo "To generate full test data, run:"
echo "  make influx-gen-data"
