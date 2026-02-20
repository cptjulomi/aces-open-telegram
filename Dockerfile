# Étape 1 : Compilation de l'application Go pour Linux
FROM golang:1.24-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

# Copie le code source Go
COPY *.go ./
# Compile le code source
RUN go build -o linux_scraper ./main.go

# Étape 2 : Lancement avec Python (pour notre Dashboard)
FROM python:3.11-alpine

WORKDIR /app

# Copie du Dashboard
COPY exe_launcher/ /app/exe_launcher/

# Installation des dépendances Flask
RUN pip install flask

# Récupération du script Go compilé
COPY --from=builder /app/linux_scraper ./linux_scraper

# IMPORTANT: Si tu as des fichiers proxy.txt ou .json de base,
# Il faut qu'ils soient récupérés par Railway ou créés via leur interface/volumes.
# On copie tout ce qui restera dans le dossier (non ignoré par .gitignore)
COPY . /app/

# Configuration du port Railway et variables
ENV PORT=5005
EXPOSE 5005

# Lancement de l'interface Dashboard
CMD ["python", "exe_launcher/app.py"]
