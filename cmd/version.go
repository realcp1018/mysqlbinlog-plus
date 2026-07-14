package cmd

import (
	"fmt"
	"mysqlbinlog-plus/internal/vars"

	"github.com/fatih/color"
)

// initVersion registers the version flag.
func initVersion() {
	rootCmd.Flags().BoolVarP(&version, "version", "V", false, fmt.Sprintf("show version of %s", vars.AppName))
}

// printVersion writes build metadata to stdout.
func printVersion() {
	fmt.Println(color.CyanString("AppVersion:"), vars.AppVersion)
	fmt.Println(color.CyanString("Go Version:"), vars.GoVersion)
	fmt.Println(color.CyanString("Build Time:"), vars.BuildTime)
	fmt.Println(color.CyanString("Git Commit:"), vars.GitCommit)
	fmt.Println(color.CyanString("Git Remote:"), vars.GitRemote)
}
