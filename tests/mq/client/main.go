// This test driver talks only to the isolated loopback RPC started by run.py.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"grpctest/internal/mqruntime"
	"grpctest/order_srv/proto"
	"os"
	"strconv"
	"time"
)

func main() {
	if os.Args[1] == "refund" {
		port, _ := strconv.Atoi(os.Getenv("TEST_MQ_PORT"))
		p, e := mqruntime.Producer(mqruntime.Config{Host: "127.0.0.1", Port: port}, "grpctest-mq-test-driver")
		if e != nil {
			panic(e)
		}
		defer p.Shutdown()
		for n := 0; n < 20; n++ {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, e = p.SendSync(ctx, primitive.NewMessage("order-reback", []byte(fmt.Sprintf(`{"OrderSn":%q}`, os.Args[2]))))
			cancel()
			if e != nil {
				panic(e)
			}
		}
		return
	}
	conn, err := grpc.NewClient(os.Getenv("TEST_ORDER_ADDRESS"), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		panic(err)
	}
	defer conn.Close()
	client := proto.NewOrderClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	request := &proto.OrderRequest{UserId: 1, RequestKey: os.Args[2], Name: "MQ测试", Address: "隔离测试", Mobile: "13000000001", Post: "MQ"}
	var rsp *proto.OrderInfoResponse
	if os.Args[1] == "create" {
		rsp, err = client.CreateOrder(ctx, request)
	} else {
		rsp, err = client.GetOrderCreation(ctx, request)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err = json.NewEncoder(os.Stdout).Encode(rsp); err != nil {
		panic(err)
	}
}
