#!/bin/sh
./tools/annotate-client/annotate-client -server https://cabinetcam.exe.xyz/ -token `cat ./secret_token`  -model qwen3-vl:8b
