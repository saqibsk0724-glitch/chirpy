# Chirpy

Chirpy is a RESTful API built with Go that allows users to create, manage, and retrieve short messages called chirps.

This project was built as part of the [Boot.dev Learn HTTP Servers in Go course](https://www.boot.dev/), with a focus on learning backend development, HTTP APIs, authentication, database integration, and webhooks.

## Features

- Create and retrieve chirps
- Retrieve all chirps or filter them by author
- Sort chirps by creation time in ascending or descending order
- User registration and login
- Password hashing
- JWT-based authentication
- Refresh and revoke authentication tokens
- Update user information
- Chirpy Red account upgrades through secure Polka webhooks
- PostgreSQL database integration
- Database migrations using Goose
- SQL query generation using SQLC

## Technologies Used

- **Go** — Backend programming language
- **PostgreSQL** — Relational database
- **SQLC** — Type-safe Go code generation from SQL queries
- **Goose** — Database migrations
- **JWT** — Authentication
- **Godotenv** — Environment variable configuration

## Getting Started

### Prerequisites

Make sure you have the following installed:

- Go
- PostgreSQL
- Goose
- SQLC

### Clone the Repository

```bash
git clone https://github.com/saqibsk0724-glitch/chirpy.git
cd chirpy