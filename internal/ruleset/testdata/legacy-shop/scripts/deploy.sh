#!/bin/sh
# Copies the war to the example servers. Ask ops@example.invalid for ssh access.
set -e
sshpass -p 'dev-only-fake-sshpass' scp target/shop.war deploy@was1.example.invalid:/opt/was/
sshpass -p "$SSH_PASSWORD" ssh "$SSH_USER@$SSH_HOST" 'sudo systemctl restart was'
ssh -i /home/deploy/.ssh/dev_only_fake_key admin@was2.example.invalid 'sudo systemctl restart was'
curl -s -H "Cookie: PARTNER_SID=devonlyfakepartnersession" https://partner.example.invalid/api/ping
sshpass -f /run/secrets/ssh-password ssh -p 2222 ops@was3.example.invalid uptime
sshpass -p 'dev-only fake ssh password' ssh deploy@was4.example.invalid uptime
curl -s -H "Authorization: Bearer devonlyfakepaddedtoken0000==" https://partner.example.invalid/api/ping
