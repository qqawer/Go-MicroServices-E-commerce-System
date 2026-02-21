# Go MicroServices E-commerce System

This repository contains the backend infrastructure for the **Go MicroServices E-commerce System**. It is designed around a highly scalable, gRPC-based microservice architecture handling everything from user accounts to order processing.

The system is decomposed into four core independent microservices:
- **`user_srv/`**: User account management, authentication, and profile handling.
- **`goods_srv/`**: Product catalog, categories, brands, and promotional banners.
- **`inventory_srv/`**: Real-time stock management, inventory locking (Sell), and restoration (Reback).
- **`order_srv/`**: Shopping cart operations and order lifecycle management.

## Key Features

- **Decoupled Microservices**: Business logic is strictly separated into User, Goods, Inventory, and Order domains.
- **High-Performance RPC**: Inter-service communication is powered by strongly-typed **gRPC** interfaces and Protocol Buffers (`.proto`).
- **Dynamic Configuration Management**: **Alibaba Nacos** is integrated to manage configurations centrally, allowing dynamic updates without service restarts.
- **Distributed Service Discovery**: Services register themselves with **HashiCorp Consul**, enabling automatic discovery, load balancing, and health-checking.
- **Relational Data Persistence**: Each service operates on top of **MySQL**, orchestrated by the **GORM** library with local connection pooling.

## Integrations (Highlights)

- **Alibaba Nacos**: Fetches real-time JSON configurations (database credentials, Consul hosts, service configurations) tailored via `namespaces`.
- **HashiCorp Consul API**: Used for agent service registration (`ServiceRegister`) and health checks.
- **Random Port Allocation**: Services can dynamically bind to free OS ports to allow horizontal scaling (multiple instances of the same service on one machine).
- **Zap Logger**: Specialized high-performance, structured logging across all services.

## Repository Structure

```text
.
├── goods_srv/               # Product catalog, brands, categories and banners
├── inventory_srv/           # Inventory counting, deductions, and rollbacks
├── order_srv/               # Shopping carts, order processing and status
├── user_srv/                # User login, passwords, and profiles
├── tmp/nacos/               # Local Nacos cache and SDK logs
├── go.mod / go.sum          # Global Go module dependencies
└── Readme.md                # Project overview map
```

### Inside each Microservice (`*_srv/`)

```text
├── config/                  # Go structure definitions mapping to Nacos JSON config
├── global/                  # Global singletons (DB connections, Logger, Nacos/Consul clients)
├── handler/                 # Controllers implementing the gRPC Server interfaces
├── initialize/              # Bootstrapping routines (Logger, Nacos, DB, SrvConn)
├── model/                   # Database models (GORM schemas)
│   └── proto/               # Protobuf templates (.proto) and generated Go stubs (.pb.go)
├── utils/                   # Shared utilities (Pagination, Hashing, Port allocation)
├── config-debug.yaml        # Local config to bootstrap connection to Nacos server
├── config-pro.yaml          # Production Nacos bootstrap config
└── main.go                  # Service entry point: binds ports, registers Consul, serves gRPC
```

## Quick Start 

Before starting the services, ensure your infrastructure dependencies (**MySQL**, **Consul**, and **Nacos**) are running and accessible. Update the `host` inside the `config-debug.yaml` of each service to point to your Nacos server instance.

Fetch go modules from the repository root:

```bash
go mod tidy
```

Run any service independently. For example, to start the Goods service on an automatically assigned port:

```bash
cd goods_srv
go run main.go
```

Alternatively, specify custom host and port bindings:

```bash
go run main.go -ip 127.0.0.1 -port 50051
```

## Manual Start (Core Dependencies)

### 1) MySQL Database
Spin up a local MySQL instance for physical data storage:
```bash
docker run -d -p 3306:3306 -e MYSQL_ROOT_PASSWORD=123456 -e MYSQL_DATABASE=grpctest --name mysql-db mysql:latest
```

### 2) HashiCorp Consul (Service Discovery)
Run Consul locally via Docker to track running instances:
```bash
docker run -d -p 8500:8500 -p 8600:8600/udp --name=consul consul agent -server -ui -node=server-1 -bootstrap-expect=1 -client=0.0.0.0
```

### 3) Alibaba Nacos (Config Center)
Start a standalone Nacos server for dynamic configuration delivery:
```bash
docker run -d -p 8848:8848 -p 9848:9848 -e MODE=standalone --name nacos-server nacos/nacos-server:v2.2.3
```
*(Ensure that Nacos is populated with the respective JSON configs matching each service's `namespace` and `dataid`)*.

## Ports

| Service            | Default Port  | Notes                                                              |
|--------------------|--------------:|--------------------------------------------------------------------|
| Go Microservices   | Random / 50051| Port dynamically scales if 0. Set explicitly via `-port` arg       |
| Consul UI / API    | 8500          | Target Consul agent host for service registry monitoring           |
| Nacos Server       | 8848          | Target Nacos server for pulling application configurations         |
| MySQL Database     | 3306          | Relational dataset connections                                     |

## Documentation Index

- **Protocol Buffers & API Contracts**
  - `user_srv/model/proto/user.proto`
  - `goods_srv/model/proto/goods.proto`
  - `inventory_srv/model/proto/inventory.proto`
  - `order_srv/proto/order.proto`

## Notes

- **Secrets & Credentials**: Each service uses a local `config-debug.yaml` heavily stripped down to only contain credentials to access the **Nacos** cluster. Database credentials and Consul host logic are securely streamed from Nacos at runtime.
- **Graceful Shutdown**: All services trap OS signals (SIGINT/SIGTERM) to cleanly deregister themselves from Consul before exiting, preventing zombie routing issues internally.
