package main

import (
	"fmt"
	"os"
	"strings"

	"velocetorrent/internal/ipc"
)

func handleCompletionCommand(args []string) error {
	shell := "bash"
	if len(args) > 0 {
		shell = strings.ToLower(strings.TrimSpace(args[0]))
	}
	switch shell {
	case "bash":
		fmt.Fprint(os.Stdout, bashCompletionScript)
	case "zsh":
		fmt.Fprint(os.Stdout, zshCompletionScript)
	case "fish":
		fmt.Fprint(os.Stdout, fishCompletionScript)
	case "powershell", "pwsh":
		fmt.Fprint(os.Stdout, pwshCompletionScript)
	default:
		return fmt.Errorf("unsupported shell %q (expected bash, zsh, fish, or powershell)", shell)
	}
	return nil
}

// handleDynamicComplete queries the live IPC daemon for dynamic <Tab> completion of active torrent hashes/names and saved directories.
func handleDynamicComplete(args []string, ipcAddr, sockPath string) error {
	target := "torrents"
	if len(args) > 0 {
		target = strings.ToLower(strings.TrimSpace(args[0]))
	}
	client := ipc.NewClient(ipcAddr, sockPath)
	snap, err := client.FetchState()
	if err != nil || snap == nil {
		return nil
	}

	switch target {
	case "dirs", "dir", "--dir", "-d":
		for _, d := range snap.SavedDirs {
			fmt.Fprintln(os.Stdout, d)
		}
	default:
		for _, t := range snap.Torrents {
			fmt.Fprintln(os.Stdout, t.InfoHash[:10])
			fmt.Fprintln(os.Stdout, t.Name)
		}
	}
	return nil
}

const bashCompletionScript = `# bash completion for VeloceTorrent (vt)
_vt_completions() {
    local cur prev words cword
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"
    local subcommands="add ui ls status pause resume rm dir priority throttle bench completion register-handlers daemon"

    if [[ ${COMP_CWORD} -eq 1 ]]; then
        COMPREPLY=( $(compgen -W "${subcommands}" -- "${cur}") )
        return 0
    fi

    case "${prev}" in
        pause|resume|rm|remove|delete|priority)
            local items=$(vt __complete torrents 2>/dev/null)
            COMPREPLY=( $(compgen -W "${items}" -- "${cur}") )
            return 0
            ;;
        dir|--dir|-d)
            local dirs=$(vt __complete dirs 2>/dev/null)
            COMPREPLY=( $(compgen -W "${dirs}" -- "${cur}") )
            return 0
            ;;
        throttle)
            COMPREPLY=( $(compgen -W "uncapped background toggle" -- "${cur}") )
            return 0
            ;;
        completion)
            COMPREPLY=( $(compgen -W "bash zsh fish powershell" -- "${cur}") )
            return 0
            ;;
    esac
}
complete -F _vt_completions vt
`

const zshCompletionScript = `#compdef vt
# zsh completion for VeloceTorrent (vt)

_vt() {
    local -a subcommands
    subcommands=(
        'add:Queue a .torrent file or magnet URI'
        'ui:Attach 60fps interactive Terminal UI'
        'ls:Print tabular transfer telemetry'
        'pause:Pause an active transfer by info_hash prefix or name'
        'resume:Resume a paused transfer by info_hash prefix or name'
        'rm:Remove a transfer from the session'
        'dir:Set or inspect saved output directories'
        'priority:Set selective file download priority (skip|normal|high)'
        'throttle:Switch bandwidth scheduler (uncapped|background|toggle)'
        'bench:Run NVMe/SSD & swarm throughput benchmark'
        'completion:Generate shell completion script'
        'register-handlers:Register magnet: and .torrent desktop handlers'
    )

    if (( CURRENT == 2 )); then
        _describe -t commands 'vt command' subcommands
        return
    fi

    case "${words[2]}" in
        pause|resume|rm|remove|priority)
            local -a torrents
            torrents=(${(f)"$(vt __complete torrents 2>/dev/null)"})
            _describe -t torrents 'active torrents' torrents
            ;;
        dir)
            local -a dirs
            dirs=(${(f)"$(vt __complete dirs 2>/dev/null)"})
            _describe -t dirs 'saved output directories' dirs
            ;;
        throttle)
            compadd uncapped background toggle
            ;;
        completion)
            compadd bash zsh fish powershell
            ;;
    esac
}
compdef _vt vt
`

const fishCompletionScript = `# fish completion for VeloceTorrent (vt)
complete -c vt -f
complete -c vt -n "__fish_use_subcommand" -a "add" -d "Queue .torrent or magnet URI"
complete -c vt -n "__fish_use_subcommand" -a "ui" -d "Attach 60fps Terminal UI"
complete -c vt -n "__fish_use_subcommand" -a "ls" -d "Print tabular transfer telemetry"
complete -c vt -n "__fish_use_subcommand" -a "pause" -d "Pause transfer by hash/name"
complete -c vt -n "__fish_use_subcommand" -a "resume" -d "Resume transfer by hash/name"
complete -c vt -n "__fish_use_subcommand" -a "rm" -d "Remove transfer"
complete -c vt -n "__fish_use_subcommand" -a "dir" -d "Set default output directory"
complete -c vt -n "__fish_use_subcommand" -a "priority" -d "Set selective file priority"
complete -c vt -n "__fish_use_subcommand" -a "throttle" -d "Switch bandwidth profile"
complete -c vt -n "__fish_use_subcommand" -a "bench" -d "Run throughput benchmark"
complete -c vt -n "__fish_use_subcommand" -a "completion" -d "Generate shell completions"

complete -c vt -n "__fish_seen_subcommand_from pause resume rm priority" -a "(vt __complete torrents 2>/dev/null)"
complete -c vt -n "__fish_seen_subcommand_from dir" -a "(vt __complete dirs 2>/dev/null)"
complete -c vt -n "__fish_seen_subcommand_from throttle" -a "uncapped background toggle"
complete -c vt -n "__fish_seen_subcommand_from completion" -a "bash zsh fish powershell"
`

const pwshCompletionScript = `# PowerShell completion for VeloceTorrent (vt)
Register-ArgumentCompleter -Native -CommandName vt,vt.exe -ScriptBlock {
    param($wordToComplete, $commandAst, $cursorPosition)
    $tokens = $commandAst.ToString().Split(' ', [System.StringSplitOptions]::RemoveEmptyEntries)
    if ($tokens.Count -le 1) {
        @('add','ui','ls','pause','resume','rm','dir','priority','throttle','bench','completion') |
            Where-Object { $_ -like "$wordToComplete*" } |
            ForEach-Object { [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_) }
        return
    }
    $sub = $tokens[1].ToLower()
    if ($sub -in @('pause','resume','rm','priority')) {
        vt __complete torrents 2>$null | Where-Object { $_ -like "$wordToComplete*" }
    } elseif ($sub -eq 'dir') {
        vt __complete dirs 2>$null | Where-Object { $_ -like "$wordToComplete*" }
    }
}
`
