# goweb

Multi domain/host web server written in Golang.

If you just want to host websites of a bunch of html/js/css files, and you don't want to be intimidated by the configuration files of Nginx or Apache, this might be for you.

## Install and Run

```sh
$ go install github.com/elgs/goweb@latest
```

Assuming you have go's bin directory in your PATH, otherwise, you could add the following line to your `.zshrc` or `.bashrc`.

```
export PATH=$HOME/go/bin:$PATH
```

Then run the server:

```sh
$ goweb -c /path/to/config.json
# if the config file happens to be goweb.json in the same directory, you could simply run:
$ goweb
# for help, run
$ goweb --help
```

or if you will listen to any ports lower than 1024:

```sh
$ sudo goweb -c /path/to/config.json
```

## Uninstall

```
$ rm -rf $HOME/go/bin/goweb
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
        "cert_path": "/path/to/certfile",
        "key_path": "/path/to/keyfile"
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
        "cert_path": "/path/to/certfile",
        "key_path": "/path/to/keyfile"
      },
      {
        "name": "example.net",
        "path": "/path/to/webroot",
        "cert_path": "/path/to/certfile",
        "key_path": "/path/to/keyfile"
      }
    ]
  }
]
```

### All parameters

#### Server

| Field    | Type   | Descriptions                                   | Examples                            |
| -------- | ------ | ---------------------------------------------- | ----------------------------------- |
| name     | string | Name of the server. Please make it unique      | 443, 80, my_server                  |
| disabled | bool   | True to disable the server, defaults to false. | false, true                         |
| type     | string | http or https                                  | http, https                         |
| listen   | string | Host and port the server listens on.           | 127.0.0.1:80, 0.0.0.0:443, [::]:443 |
| hosts    | array  | A list of hosts the server is hosting.         | See the host definition.            |

#### Host

| Field               | Type   | Descriptions                                                                         | Examples                     |
| ------------------- | ------ | ------------------------------------------------------------------------------------ | ---------------------------- |
| name                | string | Full domain name, which is used to match the domain name in the browser/request url. | example.com, www.example.com |
| disabled            | bool   | True to disable the host. Defaults to false.                                         | false, true                  |
| disable_dir_listing | bool   | True to disable dir listing if index.html file is not present. Defaults to false.    | false, true                  |
| path                | string | Path to the web root.                                                                | /path/to/webroot             |
| https_redirect_port | number | When it's greater than 0, it redirects the request to the same url but with https.   | 443                          |
| cert_path           | string | Path to the X.509 cert file.                                                         | /path/to/certfile            |
| key_path            | string | Path to the X.509 key file.                                                          | /path/to/keyfile             |

## Auto start with systemd

Create service unit file `/etc/systemd/system/goweb.service` with the following content:
```
[Unit]
After=network.target

[Service]
ExecStart=/home/elgs/go/bin/goweb -c /home/elgs/goweb.json

[Install]
WantedBy=default.target
```

Enable the service:
```sh
$ sudo systemctl enable goweb
```

Remove the service:
```sh
$ sudo systemctl disable goweb
```

Start the service
```sh
$ sudo systemctl start goweb
```

Stop the service
```sh
$ sudo systemctl stop goweb
```

Check service status
```sh
$ sudo systemctl status goweb
```