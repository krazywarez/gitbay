# Part one: an SSH key is the account. Registration mails a code, so the
# recording stops here; ship.tape picks up with the code.
Output docs/demo/out/signup/
Source docs/demo/common.tape

Type "# no account yet, just an SSH key" Enter
Sleep 800ms
Type "ssh git@gitbay.org whoami" Enter
Sleep 2.5s
Type "ssh git@gitbay.org register --username zerocool --email zerocool@gitbay.org" Enter
Sleep 4s
