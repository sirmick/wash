# Wash shakedown

A test project for Wash workspaces, run by a real orchestrator with real,
cheap members. The work is trivial on purpose (one word in each of three
files) so the tokens go on exercising Wash, not on coding: the plan graph and
its states, QA threads between agents, structured questions to the owner,
background work, nudges, acceptance trailers, and resuming from the project's
files.

## Run it

    make shakedown

copies this directory to a fresh git repository under `/tmp` and prints what
to do: open an Agent window there (the orchestrator on a frontier model, the
workspace catalog with a `small` slot, e.g. anthropic-budget) and paste

    Read SCRIPT.md and run the shakedown.

You will be asked a few questions on the way (step 7); answer them in the
panel. At the end the orchestrator runs `check.sh` and reports.

## What it checks

`SCRIPT.md` says, per step, what to do and what should happen; the
orchestrator notes every deviation. `check.sh` checks the end state on disk:
the three files, the plan file's states, the QA thread files, and the
trailers on the commit that accepted node A.
