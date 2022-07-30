# goweb
Multi domain/host web server in Golang.

### goweb.json
```json
[
  {
    "disabled": false,
    "type": "http",
    "listen": ":1080",
    "hosts": [
      {
        "name": "example.com",
        "path": "example.com"
      },
      {
        "name": "test.example.com",
        "https_redirect_port": 1443
      }
    ]
  },
  {
    "type": "https",
    "listen": "[::]:1443",
    "hosts": [
      {
        "disabled": true,
        "name": "example.com",
        "path": "example.com",
        "cert_path": "/Users/qianchen/Desktop/certs/example.com/example.com.pem",
        "key_path": "/Users/qianchen/Desktop/certs/example.com/example.com-key.pem"
      },
      {
        "name": "test.example.com",
        "path": "test.example.com",
        "cert_path": "/Users/qianchen/Desktop/certs/test.example.com/test.example.com.pem",
        "key_path": "/Users/qianchen/Desktop/certs/test.example.com/test.example.com-key.pem"
      }
    ]
  }
]
```