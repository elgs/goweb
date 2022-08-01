# goweb

Multi domain/host web server written in Golang.

If you just want to host a website of a bunch of html/js/css files, and you don't want to be intimidated by the configuration files of Nginx or Apache, this might be for you.

## Install and Run
```sh
$ go install github.com/elgs/goweb@latest
```

Assuming you have go's bin in your PATH, other wise, you could add the following line to your .zshrc or .bashrc.

```
export PATH=$HOME/go/bin:$PATH
```

Then run the server:
```sh
$ goweb goweb.json
```

or if you will listening to any ports lower than 1024:

```sh
$ sudo goweb goweb.json
```

## Configurations
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
