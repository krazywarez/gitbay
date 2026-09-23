# Part three: repository, issue, branch, merge request, merge, release,
# as the account verify.tape activated.
Output docs/demo/out/ship/
Source docs/demo/common.tape

Type "gitbay repo create zerocool/hello" Enter
Wait
Sleep 1.2s
Type "git clone -q ssh://git@gitbay.org/zerocool/hello.git && cd hello" Enter
Wait
Sleep 1.2s
Type "echo 'hello, world' > README && git add README && git commit -qm 'Say hello'" Enter
Wait
Sleep 1.2s
Type "git push -q origin main" Enter
Wait
Sleep 1.2s
Ctrl+L
Sleep 400ms

Type "gitbay issue create --title 'Say hello in French' --body 'Bonjour.'" Enter
Wait
Sleep 1.2s
Type "git switch -qc fr && echo 'bonjour, le monde' >> README" Enter
Wait
Sleep 1.2s
Type "git commit -qam 'Say bonjour' && git push -q origin fr" Enter
Wait
Sleep 1.2s
Type "gitbay mr create --source fr --target main --title Bonjour --body 'Closes #1'" Enter
Wait
Sleep 1.2s
Type "gitbay mr merge 1 --strategy squash" Enter
Wait
Sleep 1.2s
Type "gitbay issue list --state closed" Enter
Wait
Sleep 1.2s
Ctrl+L
Sleep 400ms

Type "# ship it" Enter
Sleep 600ms
Type "git switch -q main && git pull -q && git tag v0.1.0 && git push -q origin v0.1.0" Enter
Wait
Sleep 1.2s
Type "gitbay release create v0.1.0 --notes 'Hello in two languages.'" Enter
Wait
Sleep 1.2s
Type "gitbay release list" Enter
Wait
Sleep 1.2s
