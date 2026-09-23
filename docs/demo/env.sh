# Sourced by the tapes. Gives the recording a home of its own so the
# demo account's key, known_hosts, git identity and CLI profile never
# touch the operator's. OpenSSH reads ~ from the password database, not
# $HOME, so ssh is wrapped to pass -F explicitly. The home stays short:
# the CLI's ControlPath lives under it and a Unix socket path caps at 104.
DEMO_HOME=${DEMO_HOME:-$HOME/.gitbay-demo}
mkdir -p "$DEMO_HOME/.ssh" "$DEMO_HOME/bin" "$DEMO_HOME/.config/gitbay" "$DEMO_HOME/work"
chmod 700 "$DEMO_HOME/.ssh"
[ -f "$DEMO_HOME/.ssh/id_ed25519" ] ||
	ssh-keygen -q -t ed25519 -f "$DEMO_HOME/.ssh/id_ed25519" -N '' -C zerocool
[ -s "$DEMO_HOME/.ssh/known_hosts" ] ||
	ssh-keygen -F gitbay.org | grep -v '^#' > "$DEMO_HOME/.ssh/known_hosts"
printf 'Host *\n  IdentityFile %s\n  IdentitiesOnly yes\n  IdentityAgent none\n  UserKnownHostsFile %s\n' \
	"$DEMO_HOME/.ssh/id_ed25519" "$DEMO_HOME/.ssh/known_hosts" > "$DEMO_HOME/.ssh/config"
printf '#!/bin/sh\nexec /usr/bin/ssh -F "%s" "$@"\n' "$DEMO_HOME/.ssh/config" > "$DEMO_HOME/bin/ssh"
chmod +x "$DEMO_HOME/bin/ssh"
printf '[user]\n\tname = Zero Cool\n\temail = zerocool@gitbay.org\n[init]\n\tdefaultBranch = main\n' \
	> "$DEMO_HOME/.gitconfig"
printf 'default = "gitbay"\n\n[instances.gitbay]\nhost = "gitbay.org"\n' \
	> "$DEMO_HOME/.config/gitbay/config.toml"

export HOME=$DEMO_HOME PATH=$DEMO_HOME/bin:$PATH EDITOR=true
unset SSH_AUTH_SOCK XDG_CONFIG_HOME
PS1='\[\e[38;5;208m\]\W\[\e[0m\] $ '
cd "$DEMO_HOME/work"
