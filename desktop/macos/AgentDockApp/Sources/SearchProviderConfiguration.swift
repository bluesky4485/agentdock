import Foundation

// SearchProviderConfiguration 是联网搜索配置的桌面端模型，与 Core 的
// `agentdock search config show --json` 输出（脱敏视图）和
// `agentdock search config set --stdin` 输入（含 key）对应。

struct SearchProviderConfiguration: Identifiable, Equatable, Codable {
    var name: String
    var type: String
    // GUI 编辑暂存的 key。Core show 输出脱敏不回传 key；nil + hasKey = 未改动。
    var apiKey: String?
    var hasKey: Bool
    var isDefault: Bool
    var credentialReady: Bool

    // Core 的 show 输出是 snake_case 契约（key_stored/credential_ready），
    // 且 is_default 带 omitempty（false 时字段缺席），解码需逐字段兜底。
    private enum CodingKeys: String, CodingKey {
        case name, type
        case apiKey = "api_key"
        case hasKey = "key_stored"
        case isDefault = "is_default"
        case credentialReady = "credential_ready"
    }

    init(name: String, type: String, apiKey: String?, hasKey: Bool, isDefault: Bool, credentialReady: Bool) {
        self.name = name
        self.type = type
        self.apiKey = apiKey
        self.hasKey = hasKey
        self.isDefault = isDefault
        self.credentialReady = credentialReady
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        name = try container.decode(String.self, forKey: .name)
        type = try container.decode(String.self, forKey: .type)
        apiKey = try container.decodeIfPresent(String.self, forKey: .apiKey)
        hasKey = try container.decodeIfPresent(Bool.self, forKey: .hasKey) ?? false
        isDefault = try container.decodeIfPresent(Bool.self, forKey: .isDefault) ?? false
        credentialReady = try container.decodeIfPresent(Bool.self, forKey: .credentialReady) ?? false
    }

    var id: String { name.isEmpty ? "new-\(type)" : name }

    static let builtinTypes = ["tavily", "doubao", "deepseek", "exa", "kimi", "searchfree"]
}

struct SearchProviderConfigView: Equatable, Codable {
    var providers: [SearchProviderConfiguration]
    var defaultProvider: String?
    var webFetchEnabled: Bool

    private enum CodingKeys: String, CodingKey {
        case providers
        case defaultProvider = "default_provider"
        case webFetchEnabled = "web_fetch_enabled"
    }

    var anyProviderReady: Bool { providers.contains(where: \.credentialReady) }
}

enum SearchProviderConfigError: LocalizedError {
    case decoding(String)
    case commandFailed(String)

    var errorDescription: String? {
        switch self {
        case .decoding(let detail): return detail
        case .commandFailed(let detail): return detail
        }
    }
}

enum SearchProviderConfigStore {
    static func load(controller: ServiceController) async throws -> SearchProviderConfigView {
        let result = try await controller.runSearchConfigCommand(
            arguments: ["search", "config", "show", "--json"], stdin: nil)
        guard let data = result.data(using: .utf8) else {
            throw SearchProviderConfigError.decoding(L10n.text("Unable to read the search configuration."))
        }
        do {
            return try JSONDecoder().decode(SearchProviderConfigView.self, from: data)
        } catch {
            throw SearchProviderConfigError.decoding(
                L10n.text("Unable to read the search configuration."))
        }
    }

    // save 通过临时文件把含 key 的 JSON 传给 `search config set --stdin`（0600，用后即删），
    // 避免密钥出现在进程参数列表。
    static func save(
        controller: ServiceController,
        providers: [SearchProviderConfiguration],
        defaultProvider: String,
        webFetchEnabled: Bool
    ) async throws {
        let payloadProviders: [[String: Any?]] = providers
            .filter { $0.hasKey || !($0.apiKey ?? "").trimmingCharacters(in: .whitespaces).isEmpty }
            .map { provider in
                let key = provider.apiKey?.trimmingCharacters(in: .whitespaces) ?? ""
                return [
                    "name": provider.name,
                    "type": provider.type,
                    "api_key": key.isEmpty ? nil : key
                ]
            }
        let payload: [String: Any] = [
            "providers": payloadProviders,
            "default_provider": defaultProvider,
            "web_fetch_enabled": webFetchEnabled
        ]
        let data = try JSONSerialization.data(withJSONObject: payload)
        guard let json = String(data: data, encoding: .utf8) else {
            throw SearchProviderConfigError.decoding(
                L10n.text("Unable to read the search configuration."))
        }
        // set 成功后随生命周期事务重启 Core，配置才会生效；失败由调用方展示。
        try await controller.withLifecycleTransaction { lifecycle in
            let result = try await controller.runInBackground {
                try writeSearchConfigStdin(
                    executable: controller.paths.binary.path,
                    arguments: ["search", "config", "set", "--stdin"],
                    payload: json)
            }
            guard result.status == 0 else {
                throw SearchProviderConfigError.commandFailed(result.output)
            }
            _ = try await lifecycle.restart()
        }
    }
}

// writeSearchConfigStdin 把 payload 写进 0600 临时文件后作为子进程 stdin 执行。
// 与 InstallerRunner.runProcess 的差异仅在于 stdin 重定向；输出聚合 stdout+stderr。
func writeSearchConfigStdin(executable: String, arguments: [String], payload: String) throws -> ProcessExecution {
    let temporary = FileManager.default.temporaryDirectory
        .appendingPathComponent("agentdock-search-cfg-\(UUID().uuidString).json")
    try payload.data(using: .utf8)?.write(to: temporary, options: .atomic)
    try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: temporary.path)
    defer { try? FileManager.default.removeItem(at: temporary) }

    // `search config set --stdin` 从标准输入读文档。
    let process = Process()
    process.executableURL = URL(fileURLWithPath: executable)
    process.arguments = arguments
    let inputPipe = Pipe()
    let outputPipe = Pipe()
    process.standardInput = inputPipe
    process.standardOutput = outputPipe
    process.standardError = outputPipe
    try process.run()
    let data = Data(payload.utf8)
    inputPipe.fileHandleForWriting.write(data)
    inputPipe.fileHandleForWriting.closeFile()
    let outputData = outputPipe.fileHandleForReading.readDataToEndOfFile()
    process.waitUntilExit()
    return ProcessExecution(
        status: process.terminationStatus,
        output: String(data: outputData, encoding: .utf8) ?? "")
}

// SearchProvidersSettingsView 是“设置 → Web search”二级页：
