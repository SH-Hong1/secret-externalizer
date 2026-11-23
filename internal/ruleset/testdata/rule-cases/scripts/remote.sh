#!/bin/sh
export SSHPASS='dev-only-fake-sshpass-env'
sshpass -e ssh deploy@was5.example.invalid uptime
SSHPASS=dev-only-fake-sshpass-inline sshpass -e scp shop.war deploy@was6.example.invalid:/opt/was/
SSHPASS="$DEPLOY_PASSWORD" sshpass -e ssh ops@was7.example.invalid uptime
ssh ops@was8.example.invalid 'sed -i s/old/new/ /etc/app.conf'
ssh -o "StrictHostKeyChecking no" -i /home/deploy/.ssh/dev_only_fake_key2 ops@was9.example.invalid uptime
