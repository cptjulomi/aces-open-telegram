@echo off
title EXE Launcher Dashboard
echo Demarrage du serveur local...
cd /d "%~dp0"
start "Serveur Flask" /MIN cmd /c "python app.py"

echo En attente du serveur (3 secondes)...
timeout /t 3 /nobreak > nul

echo Ouverture du tableau de bord dans le navigateur...
start http://localhost:5005

echo Termine !
exit
