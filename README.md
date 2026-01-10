# Proxy-Over-SMTP

This tool will help you create a proxy service with an SMTP protocol implementation and XOR packets to confuse and disguise packets from Deep Packet Inspection (DPI) and recognize them as SMTP communications. This project is inspired by [smtp-tunnel-proxy](https://github.com/x011/smtp-tunnel-proxy)

## Getting Started

These instructions will get you a copy of the project up and running on your local machine for development and testing purposes.
See deployment section for notes on how to deploy the project on a live system.

### Prerequisites

Prequisites packages:
* Go (Go Programming Language)
* GoReleaser (Go Automated Binaries Build)
* Make (Automated Execution using Makefile)

Optional packages:
* Docker (Application Containerization)

### Deployment

#### **Using Container**

1) Install Docker CE based on the [manual documentation](https://docs.docker.com/desktop/)

2) Run the following command on your Terminal or PowerShell for the server side
```sh
docker run -d \
  -p <PROXY_SERVER_PORT>:<PROXY_SERVER_PORT>
  --name proxy-over-smtp-server \
  --rm dimaskiddo/proxy-over-smtp:latest \
  proxy-over-smtp -mode server -server <PROXY_SERVER_PORT>

# Example of Usage

docker run -d \
  -p 465:465
  --name proxy-over-smtp-server \
  --rm dimaskiddo/proxy-over-smtp:latest \
  proxy-over-smtp -mode server -server "0.0.0.0:465"
```

3) Run the following command on your Terminal or PowerShell for the client side
```sh
docker run -d \
  -p <CLIENT_PROXY_PORT>:<CLIENT_PROXY_PORT>
  --name proxy-over-smtp-client \
  --rm dimaskiddo/proxy-over-smtp:latest \
  proxy-over-smtp -mode client -client <CLIENT_PROXY_PORT> -remote <YOUR_SERVER_IP>:<PROXY_SERVER_PORT>

# Example of Usage

docker run -d \
  -p 1080:1080
  --name proxy-over-smtp-client \
  --rm dimaskiddo/proxy-over-smtp:latest \
  proxy-over-smtp -mode client -client "0.0.0.0:1080" -remote "192.168.1.100:465"
```

4) Now open your favourite browser and set the Connection setting to use Proxy

5) Set the proxy to use SOCKS version 5 protocol with address to 127.0.0.1 with port 1080 / Your Client Proxy Port

#### **Using Pre-Build Binaries**

1) Download Pre-Build Binaries from the [release page](https://github.com/dimaskiddo/proxy-over-smtp/releases)

2) Extract the zipped file

3) Run the pre-build binary for the server side
```sh
# MacOS / Linux
chmod 755 proxy-over-smtp
# -- Example of Usage
# -- ./proxy-over-smtp -mode server -server <PROXY_SERVER_PORT>
./proxy-over-smtp -mode server -server "0.0.0.0:465"

# Windows
# You can double click it or using PowerShell
# -- Example of Usage
# -- .\proxy-over-smtp.exe -mode server -server <PROXY_SERVER_PORT>
.\proxy-over-smtp.exe -mode server -server "0.0.0.0:465"
```

4) Run the pre-build binary for the client side
```sh
# MacOS / Linux
chmod 755 proxy-over-smtp
# -- Example of Usage
# -- ./proxy-over-smtp -mode client -client <CLIENT_PROXY_PORT> -remote <YOUR_SERVER_IP>:<PROXY_SERVER_PORT>
./proxy-over-smtp -mode client -client "0.0.0.0:1080" -remote "192.168.1.100:465"

# Windows
# You can double click it or using PowerShell
# -- Example of Usage
# -- .\proxy-over-smtp.exe -mode client -client <CLIENT_PROXY_PORT> -remote <YOUR_SERVER_IP>:<PROXY_SERVER_PORT>
.\proxy-over-smtp.exe -mode client -client "0.0.0.0:1080" -remote "192.168.1.100:465"
```

5) Now open your favourite browser and set the Connection setting to use Proxy

6) Set the proxy to use SOCKS version 5 protocol with address to 127.0.0.1 with port 1080 / Your Client Proxy Port

#### **Build From Source**

Below is the instructions to make this source code running:

1) Create a Go Workspace directory and export it as the extended GOPATH directory
```sh
cd <your_go_workspace_directory>
export GOPATH=$GOPATH:"`pwd`"
```

2) Under the Go Workspace directory create a source directory
```sh
mkdir -p src/github.com/dimaskiddo/proxy-over-smtp
```

3) Move to the created directory and pull codebase
```sh
cd src/github.com/dimaskiddo/proxy-over-smtp
git clone -b master https://github.com/dimaskiddo/proxy-over-smtp.git .
```

4) Run following command to pull vendor packages
```sh
make vendor
```

5) Until this step you already can run this code by using this command
```sh
make run
```

6) *(Optional)* Use following command to build this code into binary spesific platform
```sh
make build
```

7) *(Optional)* To make mass binaries distribution you can use following command
```sh
make release
```

### Running The Tests

Currently the test is not ready yet :)

## Built With

* [Go](https://golang.org/) - Go Programming Languange
* [GoReleaser](https://github.com/goreleaser/goreleaser) - Go Automated Binaries Build
* [Make](https://www.gnu.org/software/make/) - GNU Make Automated Execution
* [Docker](https://www.docker.com/) - Application Containerization

## Authors

* **Dimas Restu Hidayanto** - *Initial Work* - [DimasKiddo](https://github.com/dimaskiddo)

See also the list of [contributors](https://github.com/dimaskiddo/proxy-over-smtp/contributors) who participated in this project

## Annotation

You can seek more information for the make command parameters in the [Makefile](https://github.com/dimaskiddo/proxy-over-smtp/-/raw/master/Makefile)

## License

Copyright (C) 2026 Dimas Restu Hidayanto

Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated documentation files (the "Software"), to deal in the Software without restriction, including without limitation the rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the Software, and to permit persons to whom the Software is furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
