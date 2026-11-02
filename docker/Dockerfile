# Lightweight Node.js runtime for the generic API wrapper
FROM node:20-bookworm-slim

RUN apt-get update && apt-get install -y \
    curl \
    bash \
    build-essential \
    python3 \
    unzip \
    jq \
    git \
    && rm -rf /var/lib/apt/lists/*

# Install the Antigravity CLI
RUN curl -fsSL https://antigravity.google/cli/install.sh | bash

ENV PATH="/root/.local/bin:${PATH}"

WORKDIR /app
COPY package*.json ./
RUN npm install
COPY . .

EXPOSE 3000
CMD ["npm", "start"]
