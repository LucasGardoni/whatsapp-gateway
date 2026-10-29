// deploy e o atalho de duplo clique para o scripts/publicar.ps1: abre o
// console, mostra cada etapa do deploy e segura a janela no fim, com o
// resultado, para o erro nao sumir junto com ela.
//
// Build (o caminho do repositorio vai no binario, porque o exe fica na area
// de trabalho, longe do repositorio):
//
//	go build -ldflags "-X main.repo=C:\caminho\whatsapp-gateway" -o "Deploy Gateway.exe" ./cmd/deploy
//
// Argumentos extras vao direto para o publicar.ps1 (ex.: -SoEnviar).
package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// repo e preenchido no build (-X main.repo=...).
var repo = ""

func main() {
	os.Exit(rodar())
}

func rodar() int {
	defer esperarEnter()

	fmt.Println("======================================================================")
	fmt.Println("  DEPLOY DO WHATSAPP GATEWAY")
	fmt.Println("  testes > build > envio > migrations > troca > restart > health")
	fmt.Println("======================================================================")

	script := filepath.Join(repo, "scripts", "publicar.ps1")
	if _, err := os.Stat(script); err != nil {
		fmt.Printf("\nERRO: nao achei %s\nRecompile o exe com -X main.repo apontando para o repositorio.\n", script)
		return 1
	}

	inicio := time.Now()
	args := append([]string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script}, os.Args[1:]...)
	cmd := exec.Command("powershell.exe", args...)
	cmd.Dir = repo
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

	err := cmd.Run()
	codigo := 0
	var saida *exec.ExitError
	switch {
	case errors.As(err, &saida):
		codigo = saida.ExitCode()
	case err != nil:
		fmt.Printf("\nERRO: nao deu para iniciar o PowerShell: %v\n", err)
		return 1
	}

	duracao := time.Since(inicio).Round(time.Second)
	fmt.Println()
	fmt.Println("======================================================================")
	switch codigo {
	case 0:
		fmt.Printf("  CONCLUIDO em %s\n", duracao)
	case 2:
		fmt.Println("  CANCELADO: nada foi enviado ao servidor.")
	default:
		fmt.Printf("  FALHOU (codigo %d) depois de %s. Veja as mensagens acima.\n", codigo, duracao)
		fmt.Printf("  Log local: %s\n", filepath.Join(repo, "bin", "deploy-logs"))
		fmt.Println(`  Log no servidor: F:\www\whatsapp-gateway\logs\deploy-*.log`)
	}
	fmt.Println("======================================================================")
	return codigo
}

func esperarEnter() {
	fmt.Print("\nPressione Enter para fechar...")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}
