![Go](https://img.shields.io/badge/Go-1.23+-00ADD8?logo=go)
[![License: MIT](https://img.shields.io/badge/License-MIT-brightgreen.svg)](https://opensource.org/licenses/MIT)

# nssc (WIP)

nssc (not so simple cloud) - lightweight self-hosted cloud storage solution with multiple file server protocols.

Inspired by the [slcl](https://codeberg.org/xavidcr/slcl) project

## Features

- User management with storage quotas
- Basic authentication with `bcrypt` hashing
- Simple `JSON`-based configuration
- Per user FS separation
- Public file sharing

### Supported protocols

- `Web-based` (same as `slcl`)
- `REST API`
- `WebDAV`
- `9P/Styx` (on the way)

### Security

- Strict path validation
- Quota enforcement

## Getting Started

### Prerequisites

- Go 1.24+
- POSIX-compliant OS (Linux/macOS)

### Build

```sh
git clone https://github.com/nikita-popov/nssc.git
cd nssc
make
```

### Tests

```sh
make check
```

### Configuration

1. Initialize storage root:

```sh
mkdir -p /srv/nssc}
```

2. Add first user:

```sh
nssc adduser /srv/nssc admin p@ssw0rd 10GiB
```

3. Start server:

```sh
nssc run :8080 /srv/nssc
```

## Protocols

### Public Sharing

Shared files are accessible via: `http://{domain}/public/{uuidv7}`


### REST API

#### Endpoints

| Method | Path                      | Description                     |
|--------|------------------------|-------------------------------|
| GET    | /api/{user}/{path}       | List directory/download file  |
| PUT    | /api/{user}/{path}       | Upload file                     |
| POST   | /api/{user}/{path}/      | Create directory               |
| DELETE | /api/{user}/{path}       | Delete file/directory         |
| POST   | /api/{user}/{path}/share | Generate share link           |

#### Example Usage

```sh
# List files
curl -u user:pass http://localhost:8080/api/user/documents

# Upload file
curl -X PUT -u user:pass -T file.txt http://localhost:8080/api/user/documents/file.txt

# Create directory
curl -X POST -u user:pass http://localhost:8080/api/user?mkdir=archive

# Generate share link
curl -X POST -u user:pass http://localhost:8080/api/user/documents/file.txt/share
# Response: {"link":"/public/018f1d24-7b7f-7f3d-ae2d-c1d079e3c992"}
```

### WebDAV

Connect using any WebDAV client:

```sh
# Connection URL
http://localhost:8080/webdav/{username}

# Example with rclone
rclone mount :webdav: /mnt/nssc \
  --webdav-url http://localhost:8080 \
  --webdav-user user \
  --webdav-pass pass
```

### 9P (Styx)

Will be later.

## Bugs

Probably

## License

MIT License - see [LICENSE](LICENSE) for details.
