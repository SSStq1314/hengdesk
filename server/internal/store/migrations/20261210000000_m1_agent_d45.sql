-- D-45：智能体停用开关（可逆）。广场不展示停用的智能体；停用或删除后已有会话只读。
ALTER TABLE "agents" ADD COLUMN "disabled" boolean NOT NULL DEFAULT false;
