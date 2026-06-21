# bash completion for the wash multi-call binary.
# `wash <TAB>` completes the installed applet names (the wash-* symlinks) plus
# the dispatcher subcommands; each applet then completes its own --flags loosely.
_wash() {
    local cur prev
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"
    if [ "${COMP_CWORD}" -eq 1 ]; then
        local applets
        applets="$(cd /usr/bin 2>/dev/null && ls wash-* 2>/dev/null | sed 's/^wash-//') install-symlinks --version --help"
        COMPREPLY=( $(compgen -W "${applets}" -- "${cur}") )
        return 0
    fi
    COMPREPLY=( $(compgen -W "--help --version" -- "${cur}") )
}
complete -F _wash wash
