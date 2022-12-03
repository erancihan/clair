function host() {
    switch (process.env.NODE_ENV) {
        case "production":
            return ``;
        case "test":
        case "development":
        default:
            return `//localhost:8080`;
    }
}

export { host, host as default};
