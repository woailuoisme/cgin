// Atlas 声明式迁移配置 (事实源: internal/database/schemas/)

// 开发期比对环境：比对 schemas/ 与 migrations/ 生成版本迁移 (对应 just migrate-diff)
env "local" {
  src = "file://internal/database/schemas"
  dev = getenv("ATLAS_DEV_URL")

  migration {
    dir = "file://migrations"
  }

  format {
    migrate {
      diff = "{{ sql . \"  \" }}"
    }
  }
}

// 运行时迁移环境：将 migrations/ 应用至业务数据库 (对应 just migrate)
env "app" {
  url = getenv("DB_URL")

  migration {
    dir = "file://migrations"
  }
}

// 测试库迁移环境：将 migrations/ 应用至独立测试数据库 (对应 just migrate-test)
env "test" {
  url = getenv("TEST_DATABASE_URL")

  migration {
    dir = "file://migrations"
  }
}

