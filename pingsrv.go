// Copyright 2017 Istio Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//

// Adapted from istio/proxy/test/backend/echo with error handling and
// concurrency fixes and making it as low overhead as possible
// (no std output by default)

package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	"istio.io/fortio/fgrpc"
	"istio.io/fortio/fnet"
	"istio.io/fortio/log"
	"istio.io/fortio/stats"
	"istio.io/fortio/version"
)

// To get most debugging/tracing:
// GODEBUG="http2debug=2" GRPC_GO_LOG_VERBOSITY_LEVEL=99 GRPC_GO_LOG_SEVERITY_LEVEL=info grpcping -loglevel debug

var (
	doHealthFlag  = flag.Bool("health", false, "grpc ping client mode: use health instead of ping")
	healthSvcFlag = flag.String("healthservice", "", "which service string to pass to health check")
	payloadFlag   = flag.String("payload", "", "Payload string to send along")
)

type pingSrv struct {
}

func (s *pingSrv) Ping(c context.Context, in *fgrpc.PingMessage) (*fgrpc.PingMessage, error) {
	log.LogVf("Ping called %+v (ctx %+v)", *in, c)
	out := *in
	out.Ts = time.Now().UnixNano()
	return &out, nil
}

func pingServer(port string) {
	port = fnet.NormalizePort(port)
	socket, err := net.Listen("tcp", port)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	grpcServer := grpc.NewServer()
	reflection.Register(grpcServer)
	healthServer := health.NewServer()
	healthServer.SetServingStatus("ping", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(grpcServer, healthServer)
	fgrpc.RegisterPingServerServer(grpcServer, &pingSrv{})
	fmt.Printf("Fortio %s grpc ping server listening on port %v\n", version.Short(), port)
	if err := grpcServer.Serve(socket); err != nil {
		log.Fatalf("failed to start grpc server: %v", err)
	}
}

func pingClientCall(serverAddr string, tls bool, n int, payload string) {
	conn, err := fgrpc.Dial(serverAddr, tls)
	if err != nil {
		os.Exit(1) // error already logged
	}
	msg := &fgrpc.PingMessage{Payload: payload}
	cli := fgrpc.NewPingServerClient(conn)
	// Warm up:
	_, err = cli.Ping(context.Background(), msg)
	if err != nil {
		log.Fatalf("grpc error from Ping0 %v", err)
	}
	skewHistogram := stats.NewHistogram(-10, 2)
	rttHistogram := stats.NewHistogram(0, 10)
	for i := 1; i <= n; i++ {
		msg.Seq = int64(i)
		t1a := time.Now().UnixNano()
		msg.Ts = t1a
		res1, err := cli.Ping(context.Background(), msg)
		t2a := time.Now().UnixNano()
		if err != nil {
			log.Fatalf("grpc error from Ping1 %v", err)
		}
		t1b := res1.Ts
		res2, err := cli.Ping(context.Background(), msg)
		t3a := time.Now().UnixNano()
		t2b := res2.Ts
		if err != nil {
			log.Fatalf("grpc error from Ping2 %v", err)
		}
		rt1 := t2a - t1a
		rttHistogram.Record(float64(rt1) / 1000.)
		rt2 := t3a - t2a
		rttHistogram.Record(float64(rt2) / 1000.)
		rtR := t2b - t1b
		rttHistogram.Record(float64(rtR) / 1000.)
		midR := t1b + (rtR / 2)
		avgRtt := (rt1 + rt2 + rtR) / 3
		x := (midR - t2a)
		log.Infof("Ping RTT %d (avg of %d, %d, %d ns) clock skew %d",
			avgRtt, rt1, rtR, rt2, x)
		skewHistogram.Record(float64(x) / 1000.)
		msg = res2
	}
	skewHistogram.Print(os.Stdout, "Clock skew histogram usec", []float64{50})
	rttHistogram.Print(os.Stdout, "RTT histogram usec", []float64{50})
}

func grpcHealthCheck(serverAddr string, tls bool, svcname string, n int) {
	conn, err := fgrpc.Dial(serverAddr, tls)
	if err != nil {
		os.Exit(1) // error already logged
	}
	msg := &grpc_health_v1.HealthCheckRequest{Service: svcname}
	cli := grpc_health_v1.NewHealthClient(conn)
	rttHistogram := stats.NewHistogram(0, 10)
	statuses := make(map[grpc_health_v1.HealthCheckResponse_ServingStatus]int64)

	for i := 1; i <= n; i++ {
		start := time.Now()
		res1, err := cli.Check(context.Background(), msg)
		dur := time.Since(start)
		if err != nil {
			log.Fatalf("grpc error from Check %v", err)
		}
		statuses[res1.Status]++
		rttHistogram.Record(dur.Seconds() * 1000000.)
	}
	rttHistogram.Print(os.Stdout, "RTT histogram usec", []float64{50})
	fmt.Printf("Statuses %v\n", statuses)
}

func grpcClient() {
	if len(flag.Args()) != 1 {
		usage("Error: fortio grpcping needs host argument in the form of host, host:port or ip:port")
	}
	host := flag.Arg(0)
	count := int(*exactlyFlag)
	if count <= 0 {
		count = 1
	}
	tls := *grpcSecureFlag
	if *doHealthFlag {
		grpcHealthCheck(host, tls, *healthSvcFlag, count)
	} else {
		pingClientCall(host, tls, count, *payloadFlag)
	}
}
