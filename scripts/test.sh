GOOS=linux go build -o ./output/ordercp ./cmd/ordercp
scp ./output/ordercp nas:ordercp
