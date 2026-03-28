# Deployment Guide

## Prerequisites

- Go 1.21 or later
- SQLite 3 (included with most systems) or PostgreSQL 12+
- Docker (optional, for containerized deployment)

## Building

### From Source

```bash
# Clone repository
git clone https://github.com/l17728/pairproxy.git
cd pairproxy

# Build binary
go build -o pairproxy ./cmd/pairproxy

# Verify build
./pairproxy --version
```

### With Docker

```bash
# Build image
docker build -t pairproxy:latest .

# Run container
docker run -p 8080:8080 \
  -v /etc/pairproxy:/etc/pairproxy \
  -v /var/lib/pairproxy:/var/lib/pairproxy \
  pairproxy:latest
```

## Installation

### Linux (Systemd)

1. **Copy binary**
   ```bash
   sudo cp pairproxy /usr/local/bin/
   sudo chmod +x /usr/local/bin/pairproxy
   ```

2. **Create configuration directory**
   ```bash
   sudo mkdir -p /etc/pairproxy
   sudo cp cproxy.yaml /etc/pairproxy/
   sudo chown -R pairproxy:pairproxy /etc/pairproxy
   ```

3. **Create data directory**
   ```bash
   sudo mkdir -p /var/lib/pairproxy
   sudo chown -R pairproxy:pairproxy /var/lib/pairproxy
   ```

4. **Create systemd service**
   ```bash
   sudo tee /etc/systemd/system/pairproxy.service > /dev/null <<EOF
   [Unit]
   Description=Pairproxy LLM Proxy Service
   After=network.target

   [Service]
   Type=simple
   User=pairproxy
   WorkingDirectory=/var/lib/pairproxy
   ExecStart=/usr/local/bin/pairproxy
   Restart=on-failure
   RestartSec=10
   StandardOutput=journal
   StandardError=journal

   [Install]
   WantedBy=multi-user.target
   EOF
   ```

5. **Enable and start service**
   ```bash
   sudo systemctl daemon-reload
   sudo systemctl enable pairproxy
   sudo systemctl start pairproxy
   ```

6. **Verify service**
   ```bash
   sudo systemctl status pairproxy
   sudo journalctl -u pairproxy -f
   ```

### macOS (Homebrew)

```bash
# Install
brew install pairproxy

# Start service
brew services start pairproxy

# View logs
brew services log pairproxy
```

### Windows (PowerShell)

```powershell
# Create directory
New-Item -ItemType Directory -Path "C:\Program Files\pairproxy" -Force

# Copy binary
Copy-Item pairproxy.exe "C:\Program Files\pairproxy\"

# Create Windows service
New-Service -Name "pairproxy" `
  -BinaryPathName "C:\Program Files\pairproxy\pairproxy.exe" `
  -DisplayName "Pairproxy LLM Proxy" `
  -StartupType Automatic

# Start service
Start-Service -Name "pairproxy"
```

## Configuration for Deployment

### Development Deployment

```yaml
listen:
  host: "127.0.0.1"
  port: 8080

sproxy:
  primary: "http://localhost:9000"

log:
  level: "debug"
  format: "text"

database:
  path: "./pairproxy.db"

quota:
  enabled: false
```

### Staging Deployment

```yaml
listen:
  host: "0.0.0.0"
  port: 8080

sproxy:
  primary: "https://staging-proxy.example.com:9000"
  targets:
    - "https://staging-backup.example.com:9000"

log:
  level: "info"
  format: "json"
  output: "file"
  file_path: "/var/log/pairproxy/app.log"

database:
  driver: "postgres"
  dsn: "postgres://user:pass@staging-db.example.com/pairproxy"
  max_open_conns: 20

quota:
  enabled: true
  enforcement: "soft"
```

### Production Deployment

```yaml
listen:
  host: "0.0.0.0"
  port: 8080

sproxy:
  primary: "https://primary.prod.example.com:9000"
  targets:
    - "https://backup1.prod.example.com:9000"
    - "https://backup2.prod.example.com:9000"

log:
  level: "warn"
  format: "json"
  output: "file"
  file_path: "/var/log/pairproxy/app.log"
  max_size: 100MB
  max_backups: 10
  max_age: 30

database:
  driver: "postgres"
  dsn: "postgres://user:${DB_PASSWORD}@prod-db.example.com/pairproxy"
  max_open_conns: 50
  max_idle_conns: 20
  conn_max_lifetime: 30m

quota:
  enabled: true
  enforcement: "strict"

health_check:
  interval: 30s
  timeout: 5s

alerts:
  enabled: true
  channels:
    - "slack"
    - "email"
```

## Database Setup

### SQLite (Development)

```bash
# Database is created automatically
# Migrations run on startup
pairproxy
```

### PostgreSQL (Production)

1. **Create database**
   ```bash
   createdb pairproxy
   ```

2. **Create user**
   ```bash
   createuser pairproxy_user
   psql -c "ALTER USER pairproxy_user WITH PASSWORD 'secure_password';"
   psql -c "GRANT ALL PRIVILEGES ON DATABASE pairproxy TO pairproxy_user;"
   ```

3. **Configure connection**
   ```bash
   export CPROXY_DATABASE_DSN="postgres://pairproxy_user:secure_password@localhost/pairproxy"
   ```

4. **Run migrations**
   ```bash
   pairproxy migrate
   ```

## Health Checks

### Liveness Probe

```bash
curl http://localhost:8080/health
```

Response:
```json
{
  "status": "healthy",
  "timestamp": "2026-03-27T18:00:00Z"
}
```

### Readiness Probe

```bash
curl http://localhost:8080/ready
```

Response:
```json
{
  "ready": true,
  "database": "connected",
  "targets": 3
}
```

## Kubernetes Deployment

### Deployment Manifest

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: pairproxy
  namespace: default
spec:
  replicas: 3
  selector:
    matchLabels:
      app: pairproxy
  template:
    metadata:
      labels:
        app: pairproxy
    spec:
      containers:
      - name: pairproxy
        image: pairproxy:latest
        ports:
        - containerPort: 8080
        env:
        - name: CPROXY_LISTEN_HOST
          value: "0.0.0.0"
        - name: CPROXY_LISTEN_PORT
          value: "8080"
        - name: CPROXY_DATABASE_DSN
          valueFrom:
            secretKeyRef:
              name: pairproxy-secrets
              key: database-dsn
        livenessProbe:
          httpGet:
            path: /health
            port: 8080
          initialDelaySeconds: 10
          periodSeconds: 10
        readinessProbe:
          httpGet:
            path: /ready
            port: 8080
          initialDelaySeconds: 5
          periodSeconds: 5
        resources:
          requests:
            memory: "256Mi"
            cpu: "250m"
          limits:
            memory: "512Mi"
            cpu: "500m"
        volumeMounts:
        - name: config
          mountPath: /etc/pairproxy
      volumes:
      - name: config
        configMap:
          name: pairproxy-config
---
apiVersion: v1
kind: Service
metadata:
  name: pairproxy
spec:
  selector:
    app: pairproxy
  ports:
  - protocol: TCP
    port: 80
    targetPort: 8080
  type: LoadBalancer
```

### ConfigMap

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: pairproxy-config
data:
  cproxy.yaml: |
    listen:
      host: "0.0.0.0"
      port: 8080
    sproxy:
      primary: "https://primary.example.com:9000"
    log:
      level: "info"
      format: "json"
```

## Monitoring & Logging

### Prometheus Metrics

```bash
curl http://localhost:8080/metrics
```

Key metrics:
- `pairproxy_requests_total`: Total requests
- `pairproxy_request_duration_seconds`: Request latency
- `pairproxy_target_health`: Target health status
- `pairproxy_quota_usage`: Quota usage

### Log Aggregation

**ELK Stack:**
```yaml
filebeat:
  inputs:
  - type: log
    enabled: true
    paths:
      - /var/log/pairproxy/app.log
    json.message_key: message
    json.keys_under_root: true
```

**Datadog:**
```yaml
logs:
  - type: file
    path: /var/log/pairproxy/app.log
    service: pairproxy
    source: go
```

## Backup & Recovery

### Database Backup

**SQLite:**
```bash
sqlite3 /var/lib/pairproxy/pairproxy.db ".backup /backups/pairproxy.db.backup"
```

**PostgreSQL:**
```bash
pg_dump -U pairproxy_user pairproxy > /backups/pairproxy.sql
```

### Database Recovery

**SQLite:**
```bash
sqlite3 /var/lib/pairproxy/pairproxy.db ".restore /backups/pairproxy.db.backup"
```

**PostgreSQL:**
```bash
psql -U pairproxy_user pairproxy < /backups/pairproxy.sql
```

## Scaling

### Horizontal Scaling

1. **Deploy multiple instances** behind load balancer
2. **Use shared database** (PostgreSQL recommended)
3. **Configure health checks** for load balancer
4. **Monitor instance health** and auto-scale

### Vertical Scaling

Increase resources:
```yaml
resources:
  requests:
    memory: "512Mi"
    cpu: "500m"
  limits:
    memory: "2Gi"
    cpu: "2000m"
```

## Troubleshooting Deployment

### Service Won't Start

```bash
# Check logs
sudo journalctl -u pairproxy -n 50

# Validate configuration
pairproxy validate-config

# Check permissions
ls -la /var/lib/pairproxy
ls -la /etc/pairproxy
```

### Database Connection Issues

```bash
# Test connection
pairproxy test-db

# Check DSN
echo $CPROXY_DATABASE_DSN

# Verify database exists
psql -l | grep pairproxy
```

### High Memory Usage

```bash
# Check connection pool settings
echo $CPROXY_DATABASE_MAX_OPEN_CONNS

# Monitor memory
top -p $(pgrep pairproxy)

# Reduce pool size
export CPROXY_DATABASE_MAX_OPEN_CONNS=10
```

## Upgrade Procedure

1. **Backup database**
   ```bash
   pg_dump pairproxy > backup.sql
   ```

2. **Stop service**
   ```bash
   sudo systemctl stop pairproxy
   ```

3. **Update binary**
   ```bash
   sudo cp pairproxy /usr/local/bin/
   ```

4. **Run migrations**
   ```bash
   pairproxy migrate
   ```

5. **Start service**
   ```bash
   sudo systemctl start pairproxy
   ```

6. **Verify health**
   ```bash
   curl http://localhost:8080/health
   ```

## Rollback Procedure

1. **Stop service**
   ```bash
   sudo systemctl stop pairproxy
   ```

2. **Restore previous binary**
   ```bash
   sudo cp pairproxy.old /usr/local/bin/pairproxy
   ```

3. **Restore database backup**
   ```bash
   psql pairproxy < backup.sql
   ```

4. **Start service**
   ```bash
   sudo systemctl start pairproxy
   ```
