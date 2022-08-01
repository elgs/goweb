# goweb

Multi domain/host web server in Golang.

# Install and Run
```
go get -u github.com/elgs/goweb
```

### Bare minimum

```json
[
  {
    "type": "http",
    "listen": "[::]:80",
    "hosts": [
      {
        "name": "example.com",
        "path": "/path/to/webroot"
      }
    ]
  }
]
```

### Https with redirect

```json
[
  {
    "disabled": false,
    "type": "http",
    "listen": "[::]:80",
    "hosts": [
      {
        "name": "example.com",
        "https_redirect_port": 443
      }
    ]
  },
  {
    "type": "https",
    "listen": "[::]:443",
    "hosts": [
      {
        "name": "example.com",
        "path": "/path/to/webroot",
        "cert_path": "/path/to/cert/file",
        "key_path": "/path/to/key/file"
      }
    ]
  }
]
```

### Multiple domains

```json
[
  {
    "type": "https",
    "listen": "[::]:443",
    "hosts": [
      {
        "name": "example.com",
        "path": "/path/to/webroot",
        "cert_path": "/path/to/cert/file",
        "key_path": "/path/to/key/file"
      },
      {
        "name": "example.net",
        "path": "/path/to/webroot",
        "cert_path": "/path/to/cert/file",
        "key_path": "/path/to/key/file"
      }
    ]
  }
]
```
