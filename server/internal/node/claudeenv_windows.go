package node

// readPaneEnv reads the engine pane variables from the process's environment block (its PEB, procs_windows.go).
func readPaneEnv(pid int) map[string]string {
	if pid <= 0 {
		return nil
	}
	p, err := readWinProc(uint32(pid), observedClaudeEnvKeys)
	if err != nil {
		return nil
	}
	return p.env
}

// procEnvBytes: another agy data directory is not looked for on Windows (agyroots.go).
func procEnvBytes(int) []byte { return nil }

func managedLauncherProcessEnv(pid int) map[string]string {
	p, err := readWinProc(uint32(pid), []string{managedLaunchConfigEnv})
	if err != nil {
		return nil
	}
	return p.env
}
