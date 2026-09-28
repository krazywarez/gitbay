#!/usr/bin/env python3
"""Emit the architecture package's SVG diagrams.

Usage: python3 .gitbay/wiki/Architecture/diagrams/diagrams.py .gitbay/wiki/Architecture/diagrams
"""
import sys
from xml.sax.saxutils import escape as esc

OUT = sys.argv[1]

STYLE = """<style>
text{font-family:'Atkinson Hyperlegible Next',system-ui,-apple-system,'Segoe UI',sans-serif}
.bg{fill:#ffffff}
.t{fill:#1a1a1a;font-size:13px}
.tb1{fill:#1a1a1a;font-size:13px;font-weight:700}
.ts{fill:#4d4d4d;font-size:11px}
.th{fill:#1a1a1a;font-size:17px;font-weight:700}
.m{font-family:'Atkinson Hyperlegible Mono',ui-monospace,Menlo,monospace}
.gb{fill:#eaf0fd;stroke:#1f4fd1;stroke-width:1.4}
.ext{fill:#f3f3f3;stroke:#6b6b6b;stroke-width:1.2}
.act{fill:#ffffff;stroke:#1a1a1a;stroke-width:1.2}
.st{fill:#fff4e8;stroke:#9a3412;stroke-width:1.2}
.bad{fill:#fdecec;stroke:#b42318;stroke-width:1.2}
.ok{fill:#e8f5ec;stroke:#1a7f37;stroke-width:1.2}
.dec{fill:#ffffff;stroke:#1f4fd1;stroke-width:1.4;stroke-dasharray:5 3}
.host{fill:none;stroke:#6b6b6b;stroke-width:1.2;stroke-dasharray:3 3}
.zone{fill:none;stroke:#c2410c;stroke-width:1.6;stroke-dasharray:7 4}
.zl{fill:#c2410c;font-size:12px;font-weight:700}
.tag{fill:#c2410c;font-size:11px;font-weight:700}
.ln{stroke:#1a1a1a;stroke-width:1.2;fill:none}
.lnd{stroke:#6b6b6b;stroke-width:1.2;fill:none;stroke-dasharray:4 3}
.life{stroke:#9a9a9a;stroke-width:1;stroke-dasharray:3 4}
.ah{fill:#1a1a1a}
.ahd{fill:#6b6b6b}
@media (prefers-color-scheme: dark){
.bg{fill:#121212}
.t,.tb1,.th{fill:#ececec}
.ts{fill:#b0b0b0}
.gb{fill:#16233f;stroke:#7aa2ff}
.ext{fill:#1e1e1e;stroke:#8a8a8a}
.act{fill:#121212;stroke:#ececec}
.st{fill:#2a1a0e;stroke:#f0a36b}
.bad{fill:#2c1414;stroke:#f28b82}
.ok{fill:#122417;stroke:#6fcf8f}
.dec{fill:#121212;stroke:#7aa2ff}
.host{stroke:#8a8a8a}
.zone{stroke:#fb923c}
.zl,.tag{fill:#fb923c}
.ln{stroke:#ececec}
.lnd{stroke:#9a9a9a}
.life{stroke:#6a6a6a}
.ah{fill:#ececec}
.ahd{fill:#9a9a9a}
}
</style>
<defs>
<marker id="a" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0,0L10,5L0,10z" class="ah"/></marker>
<marker id="ad" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0,0L10,5L0,10z" class="ahd"/></marker>
</defs>"""


class SVG:
    def __init__(self, w, h, title, desc):
        self.w, self.h = w, h
        self.parts = [
            f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {w} {h}" width="{w}" height="{h}" role="img" aria-labelledby="t d">',
            f'<title id="t">{esc(title)}</title><desc id="d">{esc(desc)}</desc>',
            STYLE,
            f'<rect class="bg" x="0" y="0" width="{w}" height="{h}"/>',
            f'<text class="th" x="28" y="36">{esc(title)}</text>',
        ]

    def text(self, x, y, s, cls="t", anchor="start"):
        self.parts.append(f'<text class="{cls}" x="{x}" y="{y}" text-anchor="{anchor}">{esc(s)}</text>')

    def box(self, x, y, w, h, cls, title=None, lines=(), tcls="tb1", lcls="ts"):
        self.parts.append(f'<rect class="{cls}" x="{x}" y="{y}" width="{w}" height="{h}" rx="2"/>')
        n = (1 if title else 0) + len(lines)
        lh = 16
        cy = y + h / 2 - (n - 1) * lh / 2 + 4
        if title:
            self.text(x + w / 2, cy, title, tcls, "middle")
            cy += lh
        for ln in lines:
            self.text(x + w / 2, cy, ln, lcls, "middle")
            cy += lh

    def rect(self, x, y, w, h, cls):
        self.parts.append(f'<rect class="{cls}" x="{x}" y="{y}" width="{w}" height="{h}" rx="2"/>')

    def arrow(self, pts, cls="ln", both=False, label=None, lx=None, ly=None, lcls="ts", anchor="middle"):
        d = "M" + " L".join(f"{x},{y}" for x, y in pts)
        mk = "ad" if cls == "lnd" else "a"
        start = f' marker-start="url(#{mk})"' if both else ""
        self.parts.append(f'<path class="{cls}" d="{d}" marker-end="url(#{mk})"{start}/>')
        if label:
            if lx is None:
                (x1, y1), (x2, y2) = pts[0], pts[-1]
                lx, ly = (x1 + x2) / 2, (y1 + y2) / 2 - 5
            self.text(lx, ly, label, lcls, anchor)

    def line(self, x1, y1, x2, y2, cls):
        self.parts.append(f'<line class="{cls}" x1="{x1}" y1="{y1}" x2="{x2}" y2="{y2}"/>')

    def zone(self, x, y, w, h, label):
        self.rect(x, y, w, h, "zone")
        self.text(x + 8, y + 16, label, "zl")

    def legend(self, y, items):
        x = 28
        for cls, label in items:
            self.parts.append(f'<rect class="{cls}" x="{x}" y="{y - 11}" width="18" height="14" rx="2"/>')
            self.text(x + 24, y, label, "ts")
            x += 34 + 6.2 * len(label)

    def save(self, name):
        self.parts.append("</svg>")
        with open(f"{OUT}/{name}", "w") as f:
            f.write("\n".join(self.parts) + "\n")


LEGEND = [("gb", "gitbay"), ("act", "actor"), ("ext", "external or host"), ("st", "stored data"), ("bad", "untrusted or refused")]


def context():
    s = SVG(970, 590, "1. System context", "Actors and external systems around a gitbay instance.")
    actors = [
        ("Anonymous visitor", "HTTPS · git:// if enabled"),
        ("User: CLI or OpenSSH", "SSH :22 · public key"),
        ("User: browser", "HTTPS :443 · session cookie"),
        ("iOS app", "HTTPS API · bearer token"),
        ("CI runner", "SSH :22 · runner-scoped key"),
    ]
    tops = [70, 140, 210, 280, 350]
    targets = [125, 170, 215, 260, 305]
    for (t, sub), y, ty in zip(actors, tops, targets):
        s.box(30, y, 200, 48, "act", t, [sub])
        s.arrow([(230, y + 24), (425, ty)])
    s.box(30, 450, 200, 48, "act", "Operator", ["SSH :2222 · root"])
    s.arrow([(230, 474), (425, 474)])

    s.rect(400, 60, 290, 470, "host")
    s.text(412, 80, "Host (Linux, systemd)", "ts")
    s.rect(425, 100, 240, 300, "gb")
    s.text(545, 132, "gitbayd", "tb1", "middle")
    for i, ln in enumerate(["SSH · HTTPS · git hooks", "command registry and policy", "workers: mail, push,", "webhooks, mirrors, CI"]):
        s.text(545, 154 + i * 16, ln, "ts", "middle")
    s.box(445, 250, 200, 52, "st", "SQLite · repositories · LFS")
    s.box(445, 322, 200, 52, "ext", "git subprocesses")
    s.box(425, 450, 240, 48, "ext", "operator sshd :2222", ["keys only · fail2ban"])

    ext = [
        ("ACME CA", "TLS certificates"),
        ("SMTP relay", "mail · STARTTLS if offered"),
        ("Apple Push (APNs)", "iOS notifications"),
        ("Webhook endpoints", "HMAC-signed POSTs"),
        ("Mirror remotes", "push and pull mirrors"),
        ("Package registries", "dependency checks, opt-in"),
    ]
    etops = [70, 135, 200, 265, 330, 395]
    sources = [120, 160, 200, 240, 280, 320]
    for (t, sub), y, sy in zip(ext, etops, sources):
        s.box(750, y, 200, 48, "ext", t, [sub])
        s.arrow([(665, sy), (750, y + 24)], both=(t == "Mirror remotes"))
    s.box(750, 470, 200, 48, "ext", "Offsite object storage", ["restic · append-only key"])
    s.arrow([(690, 494), (750, 494)], cls="lnd")
    s.legend(570, LEGEND)
    s.save("01-context.svg")


def components():
    s = SVG(970, 680, "2. Components inside gitbayd", "Packages of the gitbayd daemon and how requests move between them.")
    s.box(40, 70, 250, 64, "gb", "internal/sshd", ["SSH :22 · key auth · exec · git transport"])
    s.box(310, 70, 330, 64, "gb", "internal/httpd", ["web · JSON API · smart HTTP (fetch) · LFS"])
    s.box(660, 70, 270, 64, "gb", "internal/gitd", ["git:// · upload-pack · off by default"])
    s.box(40, 190, 600, 80, "gb", "internal/control: the command registry",
          ["Dispatch: scope · read-only · disabled · admin · pending · write budget", "stdin gating · handler · audit of successful writes"])
    s.box(660, 190, 270, 80, "gb", "internal/policy", ["CanRead / CanWrite / CanAdmin", "key scopes · CheckPush · CODEOWNERS"])
    s.box(40, 320, 180, 70, "gb", "internal/hookd", ["pre- and post-receive", "decisions"])
    s.box(240, 320, 190, 70, "gb", "internal/gitutil", ["git as a subprocess", "argv only, no shell"])
    s.box(450, 320, 190, 70, "gb", "internal/sig", ["OpenPGP and SSHSIG", "verification only"])
    s.box(660, 320, 270, 70, "gb", "internal/store", ["SQLite · hand-written SQL", "59 migrations"])
    s.box(40, 440, 600, 70, "gb", "background workers",
          ["webhook delivery · mail · APNs · mirrors", "CI scheduler and stale-build reaper · dependency checks · retention sweep"])
    s.box(800, 440, 130, 70, "gb", "internal/lfs", ["content-addressed", "HMAC tokens"])
    s.box(40, 570, 180, 50, "st", "hook.sock", ["unix socket"])
    s.box(240, 570, 190, 50, "st", "repos/*.git", ["bare repositories"])
    s.box(660, 570, 120, 50, "st", "gitbay.db", ["SQLite, 0640"])
    s.box(800, 570, 130, 50, "st", "lfs/", ["objects"])

    s.arrow([(165, 134), (165, 190)])
    s.arrow([(475, 134), (475, 190)])
    s.arrow([(700, 134), (610, 190)])
    s.arrow([(640, 230), (660, 230)])
    s.arrow([(335, 270), (335, 320)], label="git transport", lx=342, ly=300, anchor="start")
    s.arrow([(545, 270), (545, 320)])
    s.arrow([(600, 270), (700, 320)])
    s.arrow([(130, 320), (130, 270)], label="decision request", lx=137, ly=300, anchor="start")
    s.arrow([(560, 440), (700, 390)])
    s.arrow([(335, 390), (335, 570)])
    s.arrow([(240, 595), (220, 595)])
    s.text(230, 560, "hooks", "ts", "middle")
    s.arrow([(120, 570), (120, 390)])
    s.arrow([(720, 390), (720, 570)])
    s.arrow([(865, 510), (865, 570)])
    s.legend(660, [("gb", "gitbayd package"), ("st", "on-disk state")])
    s.save("02-components.svg")


def deployment():
    s = SVG(970, 640, "3. Deployment (reference host)", "Processes, users, ports and files on the single gitbay host.")
    s.rect(20, 80, 180, 470, "ext")
    s.text(110, 110, "Internet", "tb1", "middle")
    for i, ln in enumerate(["clients: SSH, HTTPS", "ACME CA", "SMTP relay", "APNs", "webhook endpoints", "mirror remotes", "package registries", "offsite object storage"]):
        s.text(110, 140 + i * 22, ln, "ts", "middle")

    s.rect(240, 60, 710, 520, "host")
    s.text(252, 80, "Host: Ubuntu 24.04 · ufw inbound 22, 80, 443, 2222 · outbound open", "ts")
    s.box(280, 100, 360, 200, "gb", "gitbayd.service (user gitbay)",
          [":22 SSH · :443 HTTPS · :80 ACME and redirect", "hook.sock (unix)", "ProtectSystem=strict · NoNewPrivileges", "CAP_NET_BIND_SERVICE only · SystemCallFilter", "MemoryDenyWriteExecute · PrivateTmp"])
    s.box(680, 100, 250, 200, "st", "/var/lib/gitbay (0750)",
          ["gitbay.db (0640)", "repos/ · lfs/ · hooks/", "ssh/host_ed25519 (0600)", "acme/", "/etc/gitbay/config.toml (0640)", "/var/backups/gitbay (0750)"])
    s.box(280, 340, 360, 100, "gb", "gitbay-runner.service (user ci-runner)",
          ["polls git@127.0.0.1 over SSH with a runner key", "MemoryMax 6G · CPUQuota 300% · Delegate=yes"])
    s.box(300, 470, 320, 80, "bad", "CI containers (rootless podman)",
          ["untrusted steps · --pull=never", "build home per repository, read-write"])
    s.box(680, 340, 250, 100, "ext", "timers (user gitbay)",
          ["backup nightly · database hourly", "git gc weekly · monitor hourly", "restic to offsite storage"])
    s.box(680, 470, 250, 80, "ext", "operator sshd :2222", ["keys only · fail2ban"])

    s.arrow([(200, 170), (280, 170)], label=":22 :443 :80", lx=240, ly=163)
    s.arrow([(280, 260), (200, 260)], label="outbound", lx=240, ly=276)
    s.arrow([(640, 200), (680, 200)])
    s.arrow([(460, 340), (460, 300)], label="SSH", lx=468, ly=324, anchor="start")
    s.arrow([(460, 440), (460, 470)])
    s.arrow([(805, 340), (805, 300)])
    s.arrow([(300, 520), (200, 500)], cls="lnd", label="egress open", lx=250, ly=530)
    s.arrow([(200, 540), (255, 566), (805, 566), (805, 550)], label="SSH :2222", lx=530, ly=560)
    s.legend(620, [("gb", "gitbay unit"), ("st", "files"), ("ext", "host service"), ("bad", "untrusted code")])
    s.save("03-deployment.svg")


def trust():
    s = SVG(970, 650, "4. Trust boundaries", "Zones Z0 to Z6 and the boundaries TB1 to TB10 that data crosses between them.")
    s.zone(20, 60, 190, 560, "Z0 Internet (untrusted)")
    s.zone(250, 60, 380, 310, "Z1 gitbayd")
    s.zone(660, 60, 290, 270, "Z2 Local state")
    s.zone(250, 400, 380, 110, "Z3 git and hooks")
    s.zone(660, 400, 290, 220, "Z4 Runner")
    s.zone(675, 500, 260, 110, "Z5 Containers")
    s.zone(250, 540, 380, 80, "Z6 Operator")

    ents = [("Visitor", 90), ("User over SSH", 170), ("Browser", 250), ("API client, iOS", 330)]
    for name, y in ents:
        s.box(35, y, 160, 50, "act", name)
    s.box(35, 500, 160, 60, "ext", "Webhook and", ["mirror endpoints"])

    s.box(270, 95, 150, 55, "gb", "sshd", ["key auth"])
    s.box(270, 200, 150, 60, "gb", "httpd", ["cookie · token · CSP"])
    s.box(270, 300, 150, 50, "gb", "workers")
    s.box(450, 130, 160, 110, "gb", "Dispatch", ["handler", "resolveRepo", "policy"])

    s.box(680, 95, 250, 55, "st", "SQLite", ["token hashes · secrets in clear"])
    s.box(680, 170, 250, 55, "st", "repositories · LFS")
    s.box(680, 245, 250, 55, "st", "host key · ACME · config")

    s.box(270, 430, 150, 60, "ext", "git receive-pack", ["upload-pack"])
    s.box(450, 430, 160, 60, "ext", "gitbayd hook", ["to hook.sock"])
    s.box(680, 430, 250, 50, "gb", "gitbay-runner")
    s.box(690, 530, 230, 55, "bad", "build steps", ["repository and fork code"])
    s.box(270, 565, 340, 40, "ext", "root shell: outside every in-app control")

    s.arrow([(195, 195), (270, 122)]); s.text(232, 150, "TB1", "tag", "middle")
    s.arrow([(195, 115), (270, 215)]); s.arrow([(195, 275), (270, 232)], both=True); s.arrow([(195, 355), (270, 250)])
    s.text(232, 300, "TB2 · TB9", "tag", "middle")
    s.arrow([(420, 122), (450, 160)]); s.arrow([(420, 230), (450, 215)]); s.text(435, 190, "TB3", "tag", "middle")
    s.arrow([(610, 160), (680, 122)]); s.arrow([(610, 200), (680, 197)])
    s.arrow([(480, 240), (400, 430)]); s.text(455, 330, "TB4", "tag", "end")
    s.arrow([(420, 460), (450, 460)])
    s.arrow([(560, 430), (560, 240)]); s.text(566, 330, "TB5", "tag")
    s.arrow([(680, 450), (610, 240)], both=True); s.text(648, 320, "TB6", "tag")
    s.arrow([(805, 480), (805, 530)]); s.text(812, 510, "TB7", "tag")
    s.arrow([(270, 325), (195, 520)]); s.text(226, 460, "TB8", "tag", "middle")
    s.arrow([(690, 545), (645, 525), (195, 525)], cls="lnd", label="egress open", lx=420, ly=520)
    s.text(620, 596, "TB10", "tag", "end")
    s.legend(640, [("act", "external actor"), ("gb", "gitbay process"), ("st", "stored data"), ("bad", "untrusted code")])
    s.save("04-trust-boundaries.svg")


def authz():
    s = SVG(970, 860, "5. Authorization decision", "How a request is authorised: Dispatch gates, then repository resolution with a policy predicate, and the git transport path.")
    x, w = 40, 380
    dx, dw = 470, 190
    s.box(x, 60, w, 44, "act", "Credential", ["SSH key · API token · session cookie"])
    steps = [
        (124, "gb", "Resolve account and scope", None),
        (188, "dec", "Scope allows this command?", ("no", "denied (exit 4)")),
        (252, "dec", "Account disabled?", ("yes", "denied (exit 4)")),
        (316, "dec", "admin command and not an admin?", ("yes", "denied (exit 4)")),
        (380, "dec", "Pending account, command not allowed?", ("yes", "denied (exit 4)")),
        (444, "dec", "Write budget exhausted?", ("yes", "refused, try later")),
        (508, "gb", "Handler: resolveRepo(path, predicate)", None),
        (572, "dec", "Repository exists?", ("no", "not found (exit 3)")),
        (636, "dec", "Predicate passes? (CanRead / CanWrite / CanAdmin)", None),
        (716, "dec", "Caller can read it?", None),
    ]
    prev = 104
    for y, cls, title, deny in steps:
        s.arrow([(x + w / 2, prev), (x + w / 2, y)], label=("no" if y == 716 else None), lx=x + w / 2 + 8, ly=y - 14, anchor="start")
        s.box(x, y, w, 44, cls, title)
        if deny:
            s.arrow([(x + w, y + 22), (dx, y + 22)], label=deny[0], lx=x + w + 22, ly=y + 16)
            s.box(dx, y + 4, dw, 36, "bad", deny[1])
        prev = y + 44
    s.arrow([(x + w, 658), (dx, 658)], label="yes", lx=x + w + 22, ly=652)
    s.box(dx, 638, dw, 40, "ok", "run · audit if it wrote")
    s.arrow([(x + w, 728), (dx, 716)], label="no", lx=x + w + 20, ly=716)
    s.box(dx, 698, dw, 34, "bad", "not found (exit 3)")
    s.arrow([(x + w, 748), (dx, 760)], label="yes", lx=x + w + 20, ly=768)
    s.box(dx, 744, dw, 34, "bad", "permission denied (exit 4)")

    gx, gw = 690, 250
    s.text(gx, 76, "git transport (runGit)", "tb1")
    gsteps = [
        ("dec", "Deploy key?", "yes: only its repository and mode"),
        ("dec", "CanRead?", "no: not found"),
        ("dec", "Key scope allows git?", "runner: read only · else denied"),
        ("dec", "Push: CanWrite?", "no: denied"),
        ("dec", "Archived, pull mirror, quota", "refused"),
        ("gb", "pre-receive: CheckPush", "protected · require-mr · tags"),
        ("gb", "require-signed", "verify each incoming commit"),
        ("ok", "git applies the ref updates", None),
    ]
    y = 92
    for i, (cls, title, note) in enumerate(gsteps):
        if i:
            s.arrow([(gx + gw / 2, y - 18), (gx + gw / 2, y)])
        s.box(gx, y, gw, 52, cls, title, [note] if note else [])
        y += 70
    s.text(gx, 680, "Merges by the server skip the hooks:", "ts")
    s.text(gx, 696, "MergeGates decides them, and require-signed", "ts")
    s.text(gx, 712, "allows only fast-forward merges, so the", "ts")
    s.text(gx, 728, "server never writes an unsigned commit.", "ts")
    s.legend(835, [("dec", "check"), ("gb", "step"), ("bad", "refusal"), ("ok", "allowed")])
    s.save("05-authorization.svg")


def sequence(name, title, desc, parts, msgs, h):
    s = SVG(970, h, title, desc)
    xs = [p[0] for p in parts]
    for x, label, cls in parts:
        s.box(x - 75, 56, 150, 40, cls, label)
        s.line(x, 96, x, h - 30, "life")
    y = 130
    for m in msgs:
        a, b, text = m[0], m[1], m[2]
        style = m[3] if len(m) > 3 else "ln"
        if a == b:
            x = xs[a]
            s.arrow([(x, y - 8), (x + 30, y - 8), (x + 30, y + 6), (x + 4, y + 6)], cls=style)
            tw = 5.9 * len(text) + 8
            s.parts.append(f'<rect class="bg" x="{x + 33}" y="{y - 10}" width="{tw}" height="15"/>')
            s.text(x + 36, y + 2, text, "ts")
        else:
            x1, x2 = xs[a], xs[b]
            s.arrow([(x1, y), (x2 - 2 if x2 > x1 else x2 + 2, y)], cls=style)
            tw = 5.9 * len(text) + 8
            cx = (x1 + x2) / 2
            s.parts.append(f'<rect class="bg" x="{cx - tw / 2}" y="{y - 19}" width="{tw}" height="15"/>')
            s.text(cx, y - 7, text, "ts", "middle")
        y += 38
    s.save(name)


def push():
    parts = [(90, "Client", "act"), (250, "sshd · runGit", "gb"), (410, "git receive-pack", "ext"),
             (570, "gitbayd hook", "ext"), (730, "hookd", "gb"), (885, "policy · sig · store", "gb")]
    msgs = [
        (0, 1, "exec git-receive-pack 'owner/repo'"),
        (1, 5, "resolve repository · access · scope · quota"),
        (1, 2, "spawn with hook socket, repo, user, scope"),
        (0, 2, "pack data"),
        (2, 3, "pre-receive: ref updates on stdin"),
        (3, 4, "request over hook.sock"),
        (4, 5, "CheckPush: protected · require-mr · tags"),
        (4, 3, "need commits (if require-signed)", "lnd"),
        (3, 4, "raw commit objects", "lnd"),
        (4, 5, "VerifyCommit for each", "lnd"),
        (4, 3, "allow, or refuse with a message"),
        (3, 2, "exit 0 or 1"),
        (2, 3, "post-receive"),
        (3, 4, "post-receive request"),
        (4, 5, "events · CI queue · mirrors · MR heads · Closes #N"),
        (2, 0, "result"),
    ]
    sequence("06-push-flow.svg", "6. git push over SSH", "Sequence of a push: SSH checks, pre-receive decision by the daemon, optional signature verification, post-receive side effects.", parts, msgs, 780)


def ci():
    parts = [(90, "Pusher", "act"), (280, "gitbayd", "gb"), (470, "store", "gb"), (660, "gitbay-runner", "gb"), (860, "container", "bad")]
    msgs = [
        (0, 1, "push (post-receive)"),
        (1, 2, "queueJobs: ci/<job> pending, skipped, or reused (same tree)"),
        (3, 1, "runner next [--untrusted] · runner key, attached repositories"),
        (1, 2, "ClaimBuild: trusted builds unless --untrusted"),
        (1, 3, "claim: steps, image, secrets only if trusted"),
        (3, 1, "clone over SSH (read only)"),
        (3, 4, "podman run --pull=never · env file 0600 · build home rw"),
        (3, 4, "podman exec sh -c <step>, for each step"),
        (3, 1, "runner log <id>: streamed output"),
        (1, 2, "append log · a cancel ends the stream"),
        (3, 1, "runner done <id> success|failure"),
        (1, 2, "status ci/<job> · event · failure mail"),
        (2, 2, "scheduler reaps: log closed > 2 min, or started > 90 min"),
    ]
    sequence("07-ci-flow.svg", "7. CI build", "Sequence of a CI build from push to result, including the claim and where secrets travel.", parts, msgs, 660)


context()
components()
deployment()
trust()
authz()
push()
ci()
