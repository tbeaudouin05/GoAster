# GoAster source code evolution

Use this skill to evolve GoAster's own source code and safely update its active
runtime. Before making source changes, ask the user: "Could a simple skill meet
your need instead of changing GoAster's source code?"

## Workflow

```text
goaster-fork -> github-auth -> goaster-source -> goaster-build-test -> goaster-backup -> goaster-deploy
```
