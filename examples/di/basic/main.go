package main

import (
	"fmt"
	"log"

	"gochen-runtime/di"
	"gochen-runtime/di/basic"
)

type greetingConfig struct {
	Prefix string
}

// IGreeter 定义示例中的问候能力。
type IGreeter interface {
	Greet(name string) string
}

type greeter struct {
	config greetingConfig
}

func (g *greeter) Greet(name string) string {
	return fmt.Sprintf("%s, %s", g.config.Prefix, name)
}

type welcomeService struct {
	greeter IGreeter
}

func main() {
	container := basic.New()
	must(di.RegisterInstance(container, greetingConfig{Prefix: "hello"}))
	must(di.RegisterSingleton[IGreeter](container, func(config greetingConfig) IGreeter {
		return &greeter{config: config}
	}))
	must(di.RegisterTransient[*welcomeService](container, func(greeter IGreeter) *welcomeService {
		return &welcomeService{greeter: greeter}
	}))

	first := mustResolve[*welcomeService](container)
	second := mustResolve[*welcomeService](container)
	if first == second {
		log.Fatal("transient service unexpectedly reused")
	}
	log.Println(first.greeter.Greet("gochen"))
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func mustResolve[T any](resolver di.IResolver) T {
	value, err := di.Resolve[T](resolver)
	must(err)
	return value
}
