

default:
	go mod tidy
	go build -ldflags="-w -s"
