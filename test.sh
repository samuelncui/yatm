GOOS=linux go build -o ./output/ordercp ./cmd/ordercp
scp ./output/ordercp tape:ordercp
