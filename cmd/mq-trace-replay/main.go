// mq-trace-replay sends a synthetic duplicate to the isolated tracing broker.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"regexp"
	"time"

	"github.com/apache/rocketmq-client-go/v2/primitive"
	"grpctest/internal/mqruntime"
)

func main() {
	topic := flag.String("topic", "order-reback", "order-reback or order_timeout")
	order := flag.String("order", "", "synthetic lab order number")
	parent := flag.String("traceparent", "", "original message traceparent; empty tests legacy messages")
	flag.Parse()
	if (*topic != "order-reback" && *topic != "order_timeout") || !regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`).MatchString(*order) {
		log.Fatal("expected a lab topic and valid order number")
	}
	producer, err := mqruntime.Producer(mqruntime.Config{Enabled: true, Host: "trace-namesrv", Port: 9876}, "trace-replay")
	if err != nil {
		log.Fatal(err)
	}
	defer producer.Shutdown()
	body, _ := json.Marshal(struct{ OrderSn string }{*order})
	msg := primitive.NewMessage(*topic, body)
	msg.WithKeys([]string{*order})
	if *parent != "" {
		msg.WithProperty("traceparent", *parent)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := producer.SendSync(ctx, msg)
	if err != nil {
		log.Fatal(err)
	}
	if result == nil || result.Status != primitive.SendOK {
		log.Fatal("message not confirmed")
	}
	fmt.Println("lab duplicate stored:", *topic, result.MsgID)
}
