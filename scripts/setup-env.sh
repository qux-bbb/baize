# Update bashrc with permanent path additions
echo 'export PATH="$PATH:/c/Program Files/Go/bin:$HOME/.cargo/bin:/c/Users/q/protoc/bin:/c/Users/q/go/bin"' >> ~/.bashrc
# Set Go proxy to Chinese mirror (network issue workaround)
go env -w GOPROXY=https://goproxy.cn,direct
