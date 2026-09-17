# fish completion for every
function __every_tasks
    every list --json 2>/dev/null \
        | string match -r '"name":"[^"]*"' \
        | string replace -r '"name":"([^"]*)"' '$1'
end

# subcommands, only as the first argument
complete -c every -f -n __fish_use_subcommand -a list    -d 'status of everything'
complete -c every -f -n __fish_use_subcommand -a ls      -d 'status of everything (alias of list)'
complete -c every -f -n __fish_use_subcommand -a log     -d 'output of recent runs'
complete -c every -f -n __fish_use_subcommand -a run     -d 'run a task now'
complete -c every -f -n __fish_use_subcommand -a pause   -d 'stop scheduling'
complete -c every -f -n __fish_use_subcommand -a resume  -d 'start scheduling again'
complete -c every -f -n __fish_use_subcommand -a rm      -d 'remove a task'
complete -c every -f -n __fish_use_subcommand -a remove  -d 'remove a task (alias of rm)'
complete -c every -f -n __fish_use_subcommand -a doctor  -d "why isn't it running?"
complete -c every -f -n __fish_use_subcommand -a inspect -d 'everything about one task'
complete -c every -f -n __fish_use_subcommand -a show    -d 'everything about one task (alias of inspect)'
complete -c every -f -n __fish_use_subcommand -a exists  -d 'exit 0 if the task exists, 66 if not'
complete -c every -f -n __fish_use_subcommand -a set     -d 'add a task, or update it in place'
complete -c every -f -n __fish_use_subcommand -a schema  -d 'the JSON shape a command emits'
complete -c every -f -n __fish_use_subcommand -a version -d 'show version'
complete -c every -f -n __fish_use_subcommand -a help    -d 'show help'

# schedule keywords, also only as the first argument
complete -c every -f -n __fish_use_subcommand -a hourly   -d 'every hour'
complete -c every -f -n __fish_use_subcommand -a day      -d 'daily at a time'
complete -c every -f -n __fish_use_subcommand -a weekdays -d 'Mon-Fri'
complete -c every -f -n __fish_use_subcommand -a weekends -d 'Sat+Sun'
complete -c every -f -n __fish_use_subcommand -a monthly  -d 'on days of the month'
complete -c every -f -n __fish_use_subcommand -a once     -d 'one time only, then removes itself'
for d in monday tuesday wednesday thursday friday saturday sunday
    complete -c every -f -n __fish_use_subcommand -a $d -d 'weekly'
end

# task names for the task subcommands
complete -c every -f -n '__fish_seen_subcommand_from log run pause resume rm remove inspect show exists' -a '(__every_tasks)'

# help topics
complete -c every -f -n '__fish_seen_subcommand_from help' -a 'list ls log run pause resume rm remove doctor inspect show exists set schema version help schedules'

# flags
complete -c every -f -l json  -d 'machine-readable output'
complete -c every -f -l color -d 'auto, always or never' -x -a 'auto always never'
complete -c every -f -l help  -s h -d 'show help'
complete -c every -f -n '__fish_seen_subcommand_from log' -l with-output -d 'include captured output'
complete -c every -f -n '__fish_seen_subcommand_from log' -s n -d 'how many lines' -x
complete -c every -f -n '__fish_seen_subcommand_from run' -l dry-run -d 'what would run, without running'
complete -c every -f -l name    -d 'task name' -x
complete -c every -f -l quiet   -d 'no failure notification'
complete -c every -f -l timeout -d 'kill a run that overruns' -x
complete -c every -f -l on-fail -d 'run something when it fails' -x
