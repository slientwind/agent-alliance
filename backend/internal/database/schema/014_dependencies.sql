CREATE TABLE node_edges(project_id uuid,plan_version integer,ordinal integer,depends_on integer,
 PRIMARY KEY(project_id,plan_version,ordinal,depends_on),CHECK(depends_on<ordinal AND depends_on>0),
 FOREIGN KEY(project_id,plan_version,ordinal) REFERENCES nodes,FOREIGN KEY(project_id,plan_version,depends_on) REFERENCES nodes);
INSERT INTO node_edges SELECT project_id,plan_version,ordinal,ordinal-1 FROM nodes WHERE ordinal>1;
CREATE TABLE service_edges(service_id uuid,version integer,ordinal integer,depends_on integer,
 PRIMARY KEY(service_id,version,ordinal,depends_on),CHECK(depends_on<ordinal AND depends_on>0),
 FOREIGN KEY(service_id,version,ordinal) REFERENCES service_nodes,FOREIGN KEY(service_id,version,depends_on) REFERENCES service_nodes);
INSERT INTO service_edges SELECT service_id,version,ordinal,ordinal-1 FROM service_nodes WHERE ordinal>1;
