package main

import (
	"context"
	"log"

	"github.com/redhat-openshift-ecosystem/openshift-preflight/cmd/preflight/cmd"

	"github.com/spf13/viper"
)

func main() {
	if err := cmd.Execute(context.Background(), viper.New()); err != nil {
		log.Fatal(err)
	}
}
