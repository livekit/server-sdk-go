#!/bin/sh

mkdir -p `go env GOPATH`

if ! command -v mage &> /dev/null
then
  pushd /tmp
  git clone https://github.com/magefile/mage
  cd mage
  go run bootstrap.go
  rm -rf /tmp/mage
  popd
fi

if ! command -v mage &> /dev/null
then
  echo "Ensure `go env GOPATH`/bin is in your \$PATH"
  exit 1
fi

echo "downloading go modules"
go mod download
go install \
	"google.golang.org/protobuf/cmd/protoc-gen-go" \
	"github.com/twitchtv/twirp/protoc-gen-twirp"
