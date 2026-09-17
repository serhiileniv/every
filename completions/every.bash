# bash completion for every
_every_tasks() {
  every list --json 2>/dev/null | grep -o '"name":"[^"]*"' | sed 's/"name":"//;s/"//'
}

_every() {
  local cur prev cmds task_cmds sub schedules
  cur="${COMP_WORDS[COMP_CWORD]}"
  prev="${COMP_WORDS[COMP_CWORD-1]}"
  cmds="list ls log run pause resume rm remove doctor inspect show exists set schema version help"
  task_cmds=" log run pause resume rm remove inspect show exists "
  schedules="hourly day weekdays weekends monday tuesday wednesday thursday friday saturday sunday monthly once"

  case "$prev" in
    --color) COMPREPLY=( $(compgen -W "auto always never" -- "$cur") ); return ;;
    --timeout|--name|--on-fail|-n) return ;;
  esac

  if [ "$COMP_CWORD" -eq 1 ]; then
    COMPREPLY=( $(compgen -W "$cmds $schedules" -- "$cur") )
    return
  fi

  sub="${COMP_WORDS[1]}"
  if [[ "$cur" == -* ]]; then
    case " $sub " in
      " log ") COMPREPLY=( $(compgen -W "--json --with-output -n --color --help" -- "$cur") ) ;;
      " run ") COMPREPLY=( $(compgen -W "--json --dry-run --color --help" -- "$cur") ) ;;
      " set ") COMPREPLY=( $(compgen -W "--name --quiet --timeout --on-fail --json --color --help" -- "$cur") ) ;;
      *)
        if [[ " $cmds " == *" $sub "* ]]; then
          COMPREPLY=( $(compgen -W "--json --color --help" -- "$cur") )
        else
          COMPREPLY=( $(compgen -W "--name --quiet --timeout --on-fail --json --color --help" -- "$cur") )
        fi
        ;;
    esac
    return
  fi

  if [ "$COMP_CWORD" -eq 2 ] && [ "$sub" = "help" ]; then
    COMPREPLY=( $(compgen -W "$cmds schedules" -- "$cur") )
    return
  fi

  if [ "$COMP_CWORD" -eq 2 ] && [[ "$task_cmds" == *" $sub "* ]]; then
    COMPREPLY=( $(compgen -W "$(_every_tasks)" -- "$cur") )
  fi
}
complete -F _every every
