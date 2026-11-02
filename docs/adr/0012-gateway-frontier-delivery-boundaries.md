# Gateway, Frontier, And Delivery Boundaries

Gateway owns synchronous commands and key metadata queries; Frontier owns Frontend-facing projections and real-time SSE delivery; Frontend only renders those projections. Delivery is a Feishu channel adapter that triggers tasks, carries intermediate choices or conversation, and renders final result cards. EventCenter feeds projections and channel notifications, while Task, Assistant, and Worker remain the owners of business and execution state.
