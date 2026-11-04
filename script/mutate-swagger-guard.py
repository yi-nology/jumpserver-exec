#!/usr/bin/env python3
"""Mutation test: put each kind of drift BACK, and see whether the OpenAPI
guard (openapi_test.go) goes RED.

Why it is in the repo: "the guards were checked once" is not a thing anyone
can reproduce. This repository's rule is that a guard must prove it can go
red -- and the script that proves it has to be here.

Usage (in the repository root):

    python3 script/mutate-swagger-guard.py           # run every mutation
    python3 script/mutate-swagger-guard.py -v        # and say what each one did

Mechanism: a WHOLE-REPO COPY under /tmp, not go's -overlay.

⚠️ This is on purpose: the guard reads swagger/openapi.json at RUNTIME, and
-overlay only feeds the COMPILER -- a mutated JSON that the test still reads
from the real path never reaches the guard, and every mutant 'survives'
against unmutated input (the repo's other mutation scripts document the same
trap). So the copy is where both the Go source AND the document live, and the
real repository is never written to.

⚠️ The copy keeps its own go.work: the local go.work is machine wiring that
points ../jumpserver-kit at the sibling checkout, so the sibling is copied
alongside. A checkout without go.work degrades to module mode (the pinned
kit from the module cache) and the same script still works.

Steps:

  control   pristine copy              -> the guard MUST be green
  canary    copy + a file that does not compile
          -> the build MUST fail and the output MUST name the file.
          This is what makes the mutations meaningful: without it,
          "still green" has two causes (guard is fake / nothing ran) and
          they look identical. 不红就中止。
  mutation  copy + one revert          -> the guard MUST go red
"""
import json
import os
import shutil
import subprocess
import sys

# ⚠️ realpath, NOT abspath: macOS /tmp is a symlink to /private/tmp, and a
# path disagreement makes go tooling resolve through a different tree than
# the one we edited -- the mutation silently never lands.
ROOT = os.path.realpath(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
WORK = os.path.realpath("/tmp/jms-mutants-swagger-exec")
COPY = os.path.join(WORK, "jumpserver-exec")
KIT_SRC = os.path.realpath(os.path.join(ROOT, "..", "jumpserver-kit"))
KIT_COPY = os.path.join(WORK, "jumpserver-kit")
TEST = "^TestTheCheckedInOpenAPIDocumentMatchesTheTools$"

CANARY_FILE = "canary_broken.go"
CANARY_BODY = "package main\n\nthis line is not go and must not compile\n"


def build_mutations():
    """The mutations, anchored on the CURRENT file contents (so the script
    keeps working across releases: the anchors follow the version)."""
    doc = open(os.path.join(COPY, "swagger", "openapi.json"), encoding="utf-8").read()
    doc_ver = json.loads(doc)["info"]["version"]
    main_go = open(os.path.join(COPY, "main.go"), encoding="utf-8").read()
    bin_ver = main_go.split('const Version = "', 1)[1].split('"', 1)[0]
    return [
        {
            "id": "version-bump-no-regen",
            "file": "main.go",
            "old": 'const Version = "%s"' % bin_ver,
            "new": 'const Version = "0.0.0-stale"',
            "why": "抬了 Version 常量没重新生成文档 ⇒ 签入的契约文档的是上一版",
        },
        {
            "id": "doc-tool-path-stale",
            "file": "swagger/openapi.json",
            "old": '"/api/v1/tools/jmsx_exec/invoke"',
            "new": '"/api/v1/tools/jmsx_exec_stale/invoke"',
            "why": "文档里的工具路径与目录脱节（改了工具没重新生成文档）",
        },
        {
            "id": "doc-version-stale",
            "file": "swagger/openapi.json",
            "old": '"version": "%s"' % doc_ver,
            "new": '"version": "0.0.0-stale"',
            "why": "文档自己说的版本与二进制的版本不一致",
        },
        {
            "id": "tool-renamed-no-regen",
            "file": "biz/handler/exec.go",
            "old": '"jmsx_exec"',
            "new": '"jmsx_exec_stale"',
            "why": "工具改了名/形状，文档没重新生成",
        },
    ]


def fresh_copy():
    """One copy for the whole run: same paths across mutants means go's
    build cache actually works (a fresh directory per mutant is a cold
    build every time). The pristine bytes are captured here and written
    back before every step, so one mutant cannot leak into the next."""
    if os.path.isdir(COPY):
        shutil.rmtree(COPY)
    if os.path.isdir(KIT_COPY):
        shutil.rmtree(KIT_COPY)
    os.makedirs(WORK, exist_ok=True)
    shutil.copytree(ROOT, COPY, ignore=shutil.ignore_patterns(".git", "bin", "__pycache__"))
    if os.path.exists(os.path.join(ROOT, "go.work")) and os.path.isdir(KIT_SRC):
        shutil.copytree(KIT_SRC, KIT_COPY, ignore=shutil.ignore_patterns(".git", "bin", "__pycache__"))


def pristine_of(paths):
    out = {}
    for rel in paths:
        with open(os.path.join(COPY, rel), encoding="utf-8") as f:
            out[rel] = f.read()
    return out


def reset(pristine):
    for rel, text in pristine.items():
        with open(os.path.join(COPY, rel), "w", encoding="utf-8") as f:
            f.write(text)
    canary = os.path.join(COPY, CANARY_FILE)
    if os.path.exists(canary):
        os.remove(canary)


def run_test():
    env = dict(os.environ)
    env["GOPROXY"] = "off"
    # ⚠️ PWD must be the REAL copy path (see the realpath note above).
    env["PWD"] = COPY
    p = subprocess.run(
        ["go", "test", "-count=1", "-run", TEST, "."],
        cwd=COPY, env=env, capture_output=True, text=True,
    )
    return p.returncode, p.stdout + p.stderr


def main():
    verbose = "-v" in sys.argv[1:]
    fresh_copy()
    mutations = build_mutations()
    pristine = pristine_of({m["file"] for m in mutations})

    # control: the guard has to be green before anything is touched.
    code, out = run_test()
    if code != 0:
        print("!! 对照组就不绿（守卫本身有问题）")
        print("   " + out[:400].replace("\n", "\n   "))
        return 1
    print("✅ 【对照】未变异 ⇒ 守卫绿")

    # canary: does anything we write into the copy reach the build at all?
    # 不红就中止：canary 不炸时，每条变异的「仍绿」都不可信。
    with open(os.path.join(COPY, CANARY_FILE), "w", encoding="utf-8") as f:
        f.write(CANARY_BODY)
    code, out = run_test()
    if code == 0 or CANARY_FILE not in out:
        print("!! canary 没炸（或错误里没点名文件）⇒ 副本机制没落地，中止")
        print("   " + out[:400].replace("\n", "\n   "))
        return 1
    print("✅ 【canary】坏文件上车 ⇒ 构建红且点名 " + CANARY_FILE)
    reset(pristine)

    results = []
    for m in mutations:
        text = pristine[m["file"]]
        if text.count(m["old"]) == 0:
            print(f"!! {m['id']}: 锚点在源码里找不到（代码已变，这条变异失效）")
            results.append((m["id"], "ANCHOR-MISSING"))
            continue
        if text.count(m["old"]) != 1:
            print(f"!! {m['id']}: 锚点出现 {text.count(m['old'])} 次，不是 1 次（改脚本）")
            results.append((m["id"], "ANCHOR-AMBIGUOUS"))
            continue

        mutated = text.replace(m["old"], m["new"], 1)
        with open(os.path.join(COPY, m["file"]), "w", encoding="utf-8") as f:
            f.write(mutated)
        code, out = run_test()
        if code == 0:
            print(f"!! {m['id']}: 变异后仍然绿 ⇒ 这个守卫是假的")
            results.append((m["id"], "GUARD-FAKE"))
        else:
            print(f"✅ {m['id']}: 变异后变红（{m['why']}）")
            if verbose:
                for line in out.splitlines():
                    if "FAIL" in line or "对不上" in line:
                        print("   " + line)
            results.append((m["id"], "RED"))
        reset(pristine)

    print()
    print(f"{sum(1 for _, v in results if v == 'RED')}/{len(results)} 个变异被**测试**杀掉")
    print()
    print("=" * 64)
    counts = {}
    for _, v in results:
        counts[v] = counts.get(v, 0) + 1
    for mid, v in results:
        print(f"  {mid:32} {v}")
    bad = sum(counts.get(k, 0) for k in ("GUARD-FAKE", "ANCHOR-MISSING", "ANCHOR-AMBIGUOUS"))
    print(f"  ⇒ 抓红 {counts.get('RED', 0)} / {len(results)}；异常 {bad}")
    return 0 if bad == 0 and counts.get("RED", 0) == len(results) else 1


if __name__ == "__main__":
    sys.exit(main())
